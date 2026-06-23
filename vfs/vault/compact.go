package vault

// compact.go implements offline compaction: rewriting a container into a fresh,
// densely-packed file so freed blocks are returned to the filesystem. Under churn
// the live container reuses freed blocks (the at-rest file plateaus rather than
// growing), but it never shrinks the physical file mid-session; Compact is the
// out-of-band reclaim.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	sqlite "gosqlite.org"
)

// Compact rewrites the container at cfg.Path into a fresh, densely-packed file and
// atomically replaces the original, returning freed space to the filesystem. The
// database must NOT be open. Pass the same Options used to open it (key/recipients/
// authenticated mode); Options.Level may differ to recompress at a new level.
//
// It preserves the exact logical database and its encryption: every live page is
// re-encoded into a freshly allocated container, so the physical fragmentation that
// accumulates under churn (insert/delete/VACUUM cycles) is removed. The committed
// generation is CONTINUED, not reset, so an [Options.Anchor] stays valid across
// compaction (the anchor floor advances to the compacted generation, and any
// pre-compaction image is rejected as a rollback).
//
// Run it on a closed database; opening the same path concurrently while Compact
// runs is rejected. A crash during Compact leaves the original untouched (the new
// container is written to a temp sibling and renamed only once durable).
//
// For a multi-recipient database (Options.Recipients), the compacted file is
// wrapped under a FRESH random data key — Compact doubles as a key rotation, like
// [Rekey] — so pass the Identities needed to read the source and the Recipients
// (and Masters/SignWith for writer-signed mode) to seal the new keyslot. A raw-key
// database keeps its key.
func Compact(cfg sqlite.Config, opts Options) error {
	if cfg.Path == "" || cfg.Path == sqlite.InMemory || cfg.Mode == sqlite.ModeMemory {
		return errors.New("vault: Compact requires an on-disk Config.Path")
	}
	if cfg.VFS != "" {
		return errors.New("vault: Compact sets the VFS itself; leave Config.VFS empty")
	}
	kc, err := keyConfigFromOptions(opts)
	if err != nil {
		return err
	}
	blockSize, pageSize, err := opts.resolveLive()
	if err != nil {
		return err
	}

	// Refuse to compact a path that is open in this process: Compact drives the
	// file through its own unregistered container, so a second live container over
	// the same file would race two writers onto it.
	abs, err := filepath.Abs(cfg.Path)
	if err != nil {
		return err
	}
	containers.mu.Lock()
	open := containers.m[abs] != nil
	containers.mu.Unlock()
	if open {
		return fmt.Errorf("vault: Compact: database %q is open; close it first", cfg.Path)
	}

	srcFile, err := os.Open(cfg.Path)
	if err != nil {
		return err
	}
	src, err := newContainerOver(fileBacking{srcFile}, true, blockSize, pageSize, opts.Level, kc)
	if err != nil {
		return err // newContainerOver closes srcFile on error
	}
	srcClosed := false
	closeSrc := func() {
		if !srcClosed {
			_ = src.back.Close()
			srcClosed = true
		}
	}
	defer closeSrc()

	tmp, err := os.CreateTemp(filepath.Dir(cfg.Path), "."+filepath.Base(cfg.Path)+".compact-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// The temp container starts empty; do NOT run the anchor's empty-file rollback
	// check against it (the floor is legitimately above an empty temp file). The
	// real floor is advanced by the data commit below, via dst.anchor.
	dstKc := kc
	dstKc.anchor = nil
	dst, err := newContainerOver(fileBacking{tmp}, false, blockSize, pageSize, opts.Level, dstKc)
	if err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	cleanup := func(e error) error {
		_ = dst.back.Close()
		_ = os.Remove(tmpPath)
		return e
	}

	// Continue the generation sequence so the compacted file is NEWER than every
	// pre-compaction image — keeping an external replay anchor monotonic and valid.
	// Attach the anchor now (after the empty create commit) so only the data commit
	// records the continued generation.
	dst.anchor = kc.anchor
	if src.committedGen > dst.committedGen {
		dst.committedGen = src.committedGen
	}

	// Copy every logical page (the slot cipher tweak is the page number, so a page
	// re-encodes correctly into the fresh, densely-packed layout). The directory's
	// freed-block holes are simply not carried over.
	buf := make([]byte, pageSize)
	for p := uint64(0); p < src.pageCount; p++ {
		if _, err := src.readAt(buf, int64(p*pageSize)); err != nil {
			return cleanup(fmt.Errorf("vault: Compact read page %d: %w", p, err))
		}
		if _, err := dst.writeAt(buf, int64(p*pageSize)); err != nil {
			return cleanup(fmt.Errorf("vault: Compact write page %d: %w", p, err))
		}
	}
	dst.dirty = true
	if err := dst.commit(); err != nil {
		return cleanup(fmt.Errorf("vault: Compact commit: %w", err))
	}
	if err := dst.back.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	closeSrc()

	if err := os.Rename(tmpPath, cfg.Path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if d, err := os.Open(filepath.Dir(cfg.Path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
