package blobstore

import (
	"context"
	"database/sql"
	"fmt"
)

// Clone creates a new object whose content is identical to srcID and returns its
// id. It copies no bytes: only the object row and the (seq -> block) mappings are
// duplicated, and the shared blocks' reference counts are bumped — so it is
// O(object metadata) regardless of object size, with no BLOB I/O. The two
// objects then diverge copy-on-write: a write to either copies only the touched
// chunks' blocks, leaving the other unchanged. Returns [ErrNotFound] if srcID
// does not exist.
//
// Clone is the foundation for cheap snapshots (an external snapshot namespace)
// and for [Store.NewVersion]; both are O(metadata) because of it.
func (s *Store) Clone(ctx context.Context, srcID int64) (int64, error) {
	var dstID int64
	err := s.withTx(ctx, func(sc *sql.Conn) error {
		id, existed, err := s.cloneObjectTx(ctx, sc, srcID)
		if err != nil {
			return fmt.Errorf("blobstore: clone %d: %w", srcID, err)
		}
		if !existed {
			return fmt.Errorf("blobstore: clone %d: %w", srcID, ErrNotFound)
		}
		dstID = id
		return nil
	})
	if err != nil {
		return 0, err
	}
	return dstID, nil
}

// cloneObjectTx duplicates object srcID inside the open transaction sc and
// returns the new id and whether srcID existed. It copies the object row (size,
// chunk, mode, retention) and every chunk mapping, then adds one reference to
// each referenced block per new mapping — count-correct even if two chunks share
// one block. Shared by Clone and version snapshotting.
func (s *Store) cloneObjectTx(ctx context.Context, sc *sql.Conn, srcID int64) (int64, bool, error) {
	// Stamp created_at with the store clock, like Create — a clone (and the
	// version snapshot built on it) is a new object born now, NOT a copy of the
	// source's age. This keeps 0 meaning only "back-filled legacy row", which the
	// age-based sweep relies on; copying the source's time would let a fresh clone
	// read as arbitrarily old.
	res, err := sc.ExecContext(ctx,
		`INSERT INTO `+s.objs+` (size, chunk, codec, level, keep_versions, max_age, created_at) `+
			`SELECT size, chunk, codec, level, keep_versions, max_age, ? FROM `+s.objs+` WHERE id = ?`,
		s.now().UnixNano(), srcID)
	if err != nil {
		return 0, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, nil // srcID does not exist
	}
	dstID, err := res.LastInsertId()
	if err != nil {
		return 0, false, err
	}
	if _, err := sc.ExecContext(ctx,
		`INSERT INTO `+s.chunks+` (obj, seq, block) SELECT ?, seq, block FROM `+s.chunks+` WHERE obj = ?`,
		dstID, srcID); err != nil {
		return 0, false, err
	}
	if _, err := sc.ExecContext(ctx,
		`UPDATE `+s.blocks+` SET refs = refs + m.cnt `+
			`FROM (SELECT block, count(*) AS cnt FROM `+s.chunks+` WHERE obj = ? GROUP BY block) m `+
			`WHERE `+s.blocks+`.id = m.block`,
		dstID); err != nil {
		return 0, false, err
	}
	return dstID, true, nil
}
