package blobstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// A version is a copy-on-write snapshot of an object's chunk mapping at a point
// in time, taken with [Store.NewVersion] and reusing [Store.Clone]: it shares
// every block with the live object and diverges only as the live object is
// written, so a version costs O(metadata) and grows storage only by what
// changes. Versions are read with [Store.OpenVersion]; retention is governed by
// a per-object [Policy] ([Store.SetRetention]) and applied by [Store.Prune] and
// by the sweep that runs after each NewVersion.

// VersionInfo describes one stored version of an object.
type VersionInfo struct {
	VersionNo int64     // monotonically increasing per object, starting at 1
	CreatedAt time.Time // when the version was taken (store clock)
	Label     string    // optional caller label ("" if none)
	Size      int64     // logical size of the object at the time of the snapshot
}

// VersionOption customizes a single [Store.NewVersion].
type VersionOption func(*versionConfig)

type versionConfig struct {
	label    string
	hasLabel bool
}

// WithLabel attaches a caller-defined label to a new version, surfaced by
// [Store.ListVersions].
func WithLabel(label string) VersionOption {
	return func(vc *versionConfig) { vc.label = label; vc.hasLabel = true }
}

// NewVersion snapshots object objID's current content as a new version and
// returns its version number (1 for the first, increasing thereafter). The
// snapshot shares all blocks with the live object copy-on-write, so it is
// O(metadata). After recording it, the object's retention [Policy] is applied
// (see [Store.SetRetention]). Returns [ErrNotFound] if objID does not exist, and
// [ErrReadOnly] on a read-only store.
func (s *Store) NewVersion(ctx context.Context, objID int64, opts ...VersionOption) (int64, error) {
	var vc versionConfig
	for _, o := range opts {
		o(&vc)
	}
	var versionNo int64
	err := s.withTx(ctx, func(sc *sql.Conn) error {
		snap, existed, err := s.cloneObjectTx(ctx, sc, objID)
		if err != nil {
			return fmt.Errorf("blobstore: NewVersion %d: %w", objID, err)
		}
		if !existed {
			return fmt.Errorf("blobstore: NewVersion %d: %w", objID, ErrNotFound)
		}
		if err := sc.QueryRowContext(ctx,
			`SELECT coalesce(max(version_no), 0) + 1 FROM `+s.versions+` WHERE obj = ?`, objID).Scan(&versionNo); err != nil {
			return fmt.Errorf("blobstore: NewVersion %d: %w", objID, err)
		}
		var label any
		if vc.hasLabel {
			label = vc.label
		}
		if _, err := sc.ExecContext(ctx,
			`INSERT INTO `+s.versions+` (obj, version_no, created_at, snapshot_obj, label) VALUES (?, ?, ?, ?, ?)`,
			objID, versionNo, s.now().UnixNano(), snap, label); err != nil {
			return fmt.Errorf("blobstore: NewVersion %d: %w", objID, err)
		}
		return s.pruneTx(ctx, sc, objID)
	})
	if err != nil {
		return 0, err
	}
	s.maybeVacuum(ctx)
	return versionNo, nil
}

// ListVersions returns object objID's versions in version-number order (empty if
// it has none). It does not error on an unknown object — an object with no
// versions and a missing object both list as empty.
func (s *Store) ListVersions(ctx context.Context, objID int64) ([]VersionInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT v.version_no, v.created_at, coalesce(v.label, ''), o.size `+
			`FROM `+s.versions+` v JOIN `+s.objs+` o ON o.id = v.snapshot_obj `+
			`WHERE v.obj = ? ORDER BY v.version_no`, objID)
	if err != nil {
		return nil, fmt.Errorf("blobstore: ListVersions %d: %w", objID, err)
	}
	defer rows.Close()
	var out []VersionInfo
	for rows.Next() {
		var vi VersionInfo
		var createdAt int64
		if err := rows.Scan(&vi.VersionNo, &createdAt, &vi.Label, &vi.Size); err != nil {
			return nil, fmt.Errorf("blobstore: ListVersions %d: %w", objID, err)
		}
		vi.CreatedAt = time.Unix(0, createdAt)
		out = append(out, vi)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("blobstore: ListVersions %d: %w", objID, err)
	}
	return out, nil
}

// OpenVersion returns a read-only [Reader] over version versionNo of object
// objID. The snapshot is immutable — nothing ever writes to it — so reads are
// stable for the life of the handle. Works on a read-only store. Returns
// [ErrNotFound] if the object or version does not exist.
func (s *Store) OpenVersion(ctx context.Context, objID, versionNo int64) (*Reader, error) {
	var snap int64
	err := s.db.QueryRowContext(ctx,
		`SELECT snapshot_obj FROM `+s.versions+` WHERE obj = ? AND version_no = ?`, objID, versionNo).Scan(&snap)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("blobstore: OpenVersion %d v%d: %w", objID, versionNo, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("blobstore: OpenVersion %d v%d: %w", objID, versionNo, err)
	}
	return s.Reader(ctx, snap)
}

// SetRetention sets object objID's version-retention [Policy]. It records the
// policy only; it does not prune existing versions — call [Store.Prune] (or take
// a new version) to apply a tightened policy. Returns [ErrNotFound] if objID does
// not exist, [ErrReadOnly] on a read-only store.
func (s *Store) SetRetention(ctx context.Context, objID int64, p Policy) error {
	return s.withTx(ctx, func(sc *sql.Conn) error {
		res, err := sc.ExecContext(ctx,
			`UPDATE `+s.objs+` SET keep_versions = ?, max_age = ? WHERE id = ?`,
			p.KeepVersions, int64(p.MaxAge), objID)
		if err != nil {
			return fmt.Errorf("blobstore: SetRetention %d: %w", objID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("blobstore: SetRetention %d: %w", objID, err)
		}
		if n == 0 {
			return fmt.Errorf("blobstore: SetRetention %d: %w", objID, ErrNotFound)
		}
		return nil
	})
}

// Prune applies object objID's retention [Policy] now, deleting versions that
// fall outside it and freeing the blocks their snapshots uniquely held. It is a
// no-op for an object with an unlimited policy or no versions. Returns
// [ErrReadOnly] on a read-only store.
func (s *Store) Prune(ctx context.Context, objID int64) error {
	if err := s.withTx(ctx, func(sc *sql.Conn) error {
		return s.pruneTx(ctx, sc, objID)
	}); err != nil {
		return err
	}
	s.maybeVacuum(ctx)
	return nil
}

// pruneTx deletes object objID's versions that fall outside its retention policy
// within the open transaction sc: a version is removed if it is beyond the
// newest keep_versions OR older than max_age (0 disables each bound). Each
// removed version's hidden snapshot clone is deleted, releasing the blocks it
// alone held.
func (s *Store) pruneTx(ctx context.Context, sc *sql.Conn, objID int64) error {
	var keep, maxAge int64
	err := sc.QueryRowContext(ctx,
		`SELECT keep_versions, max_age FROM `+s.objs+` WHERE id = ?`, objID).Scan(&keep, &maxAge)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // object gone — nothing to prune
	}
	if err != nil {
		return err
	}
	if keep <= 0 && maxAge <= 0 {
		return nil // unlimited
	}

	// Collect the doomed versions first (cursor closed before any delete, since
	// one connection runs one statement at a time).
	rows, err := sc.QueryContext(ctx,
		`SELECT version_no, snapshot_obj, created_at FROM `+s.versions+` WHERE obj = ? ORDER BY version_no DESC`, objID)
	if err != nil {
		return err
	}
	cutoff := s.now().UnixNano() - maxAge
	type doomedVersion struct{ versionNo, snapshot int64 }
	var doomed []doomedVersion
	rank := int64(0)
	for rows.Next() {
		var versionNo, snapshot, createdAt int64
		if err := rows.Scan(&versionNo, &snapshot, &createdAt); err != nil {
			rows.Close()
			return err
		}
		rank++
		drop := (keep > 0 && rank > keep) || (maxAge > 0 && createdAt < cutoff)
		if drop {
			doomed = append(doomed, doomedVersion{versionNo, snapshot})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, d := range doomed {
		if _, err := s.deleteObjectTx(ctx, sc, d.snapshot); err != nil {
			return err
		}
		if _, err := sc.ExecContext(ctx,
			`DELETE FROM `+s.versions+` WHERE obj = ? AND version_no = ?`, objID, d.versionNo); err != nil {
			return err
		}
	}
	return nil
}

// deleteVersionsTx removes every version of object objID and the hidden snapshot
// clone behind each, within the open transaction sc. It is the version cascade
// for Delete, so deleting an object never leaves orphaned snapshots holding
// block references.
func (s *Store) deleteVersionsTx(ctx context.Context, sc *sql.Conn, objID int64) error {
	rows, err := sc.QueryContext(ctx,
		`SELECT snapshot_obj FROM `+s.versions+` WHERE obj = ?`, objID)
	if err != nil {
		return err
	}
	var snaps []int64
	for rows.Next() {
		var snap int64
		if err := rows.Scan(&snap); err != nil {
			rows.Close()
			return err
		}
		snaps = append(snaps, snap)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, snap := range snaps {
		if _, err := s.deleteObjectTx(ctx, sc, snap); err != nil {
			return err
		}
	}
	_, err = sc.ExecContext(ctx, `DELETE FROM `+s.versions+` WHERE obj = ?`, objID)
	return err
}
