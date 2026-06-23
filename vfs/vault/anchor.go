package vault

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

// ErrRolledBack reports that a database was opened whose committed generation is
// below the floor recorded by its [ReplayAnchor] — i.e. the on-disk file is a
// complete, validly-signed, but STALE earlier state that an attacker (or a backup
// restore) rolled the database back to. It is returned only when [Options.Anchor]
// is set.
var ErrRolledBack = errors.New("vault: database rolled back below the replay-anchor floor")

// ReplayAnchor is an external, monotonic record of the highest committed
// generation a database has reached, kept OUTSIDE the database file. Supplying one
// via [Options.Anchor] upgrades authenticated mode from tamper-evident to
// rollback-RESISTANT: authenticated mode already binds the generation into the
// signed root (so a state cannot be renumbered), and the anchor adds the missing
// piece — a floor the file cannot drag backwards. Opening a database whose
// committed generation is below the recorded floor fails with [ErrRolledBack].
//
// The anchor is only as trustworthy as where it lives. A counter in a TPM, an HSM,
// a secure enclave, or a server the attacker cannot also roll back gives real
// protection; a sidecar file on the SAME disk does not (an attacker who can
// rewrite the database can rewrite the sidecar too). The store must be durable and
// strictly monotonic.
//
// Anchored mode requires authenticated mode ([Options.Authenticate] or
// [Options.Writers]): without an authenticated generation the floor is forgeable.
type ReplayAnchor interface {
	// LoadGeneration returns the highest generation recorded, or 0 if none has been
	// stored yet (a fresh anchor for a fresh database).
	LoadGeneration() (uint64, error)
	// StoreGeneration records gen as the new floor. It is called after a commit has
	// made that generation durable, so it MUST be monotonic (never lower the stored
	// value) and durable before it returns.
	StoreGeneration(gen uint64) error
}

// FileAnchor stores the generation floor as a small text file at path, written
// atomically and monotonically.
//
// SECURITY: a file anchor only resists rollback if path lives on storage the
// attacker cannot roll back together with the database — a separate device, a
// network mount, a keystore-backed filesystem. On the same disk as the database it
// documents intent but stops nothing, because the same write primitive that
// reverts the database reverts the sidecar. For a real trust anchor, implement
// [ReplayAnchor] over a TPM/HSM monotonic counter or a remote service.
func FileAnchor(path string) ReplayAnchor { return &fileAnchor{path: path} }

type fileAnchor struct {
	mu   sync.Mutex
	path string
}

func (f *fileAnchor) LoadGeneration() (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadLocked()
}

func (f *fileAnchor) loadLocked() (uint64, error) {
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	gen, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("vault: malformed replay anchor %q: %w", f.path, err)
	}
	return gen, nil
}

func (f *fileAnchor) StoreGeneration(gen uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, err := f.loadLocked()
	if err != nil {
		return err
	}
	if gen <= cur {
		return nil // monotonic: never move the floor backwards
	}
	return atomicWrite(f.path, func(w io.Writer) error {
		_, err := io.WriteString(w, strconv.FormatUint(gen, 10))
		return err
	})
}
