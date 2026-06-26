package vault

// trim.go is the online, relocation-free space reclaim: returning trailing free
// blocks to the OS while the database stays open. Under churn the live container
// reuses freed blocks and never shrinks the physical file mid-session by design;
// Trim is the cheap "shrink while mounted" path for the common case where free
// blocks have accumulated at the tail (e.g. after a delete). Offline Compact stays
// the densest reclaim (it relocates live blocks); a full live relocate-under-COW
// compaction is a larger future follow-up.

import (
	"fmt"
	"path/filepath"

	sqlite "gosqlite.org"
)

// Checkpoint folds the write-ahead log back into the container and returns any
// freed trailing blocks to the OS, while the database stays open — the "shrink a
// mounted container" operation, combining a WAL checkpoint with [Trim] in one call.
// It runs PRAGMA wal_checkpoint(TRUNCATE) on db, then Trim on path; db and path
// must be the same database (the one path it was opened with). It returns the bytes
// Trim reclaimed.
//
// Because the directory is segmented, the per-checkpoint container commit re-encodes
// only the segments the fold touched, so the checkpoint holds the write lock for a
// bounded slice rather than the whole directory. On a rollback-journal database the
// checkpoint is a no-op and only Trim applies. TRUNCATE briefly needs the write lock;
// open the database with a busy timeout (e.g. [gosqlite.org.OpenWAL]) so it queues
// rather than failing under contention.
func Checkpoint(db *sqlite.DB, path string) (reclaimed int64, err error) {
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return 0, fmt.Errorf("vault: checkpoint: %w", err)
	}
	return Trim(path, 0)
}

// ReclaimableBytes reports how many bytes a compaction of the OPEN container at path
// could return to the OS: the space currently allocated to the file but held on the
// free list, not used by live data. It is what a full [Compact] / [CompactLogicalOnline]
// would recover; [Trim] alone recovers only the part of it that sits at the tail. It is
// cheap (an allocator sum, no I/O) and a snapshot — a concurrent writer can change it
// the instant it returns — so use it to decide whether a reclaim pass is worthwhile and
// to show progress, not as an exact promise. path must name a database currently open
// in THIS process.
func ReclaimableBytes(path string) (int64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	containers.mu.Lock()
	c := containers.m[abs]
	if c != nil {
		c.refs++ // pin the container against a concurrent last-handle Close
	}
	containers.mu.Unlock()
	if c == nil {
		return 0, fmt.Errorf("vault: ReclaimableBytes: no open database at %q", path)
	}
	defer func() { _ = c.release() }()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return int64(c.alloc.freeBlocksTotal() * c.blockSize), nil
}

// Trim returns trailing free blocks of the OPEN container at path to the OS,
// shrinking the file while the database stays open, and reports the bytes
// reclaimed. It is safe to call from a background goroutine and cheap (a single
// truncate, no page relocation): it recovers space only when free blocks sit at
// the very tail of the file — common after deletes — and recovers nothing when the
// tail is still in use. For the densest reclaim, close the database and run
// [Compact].
//
// maxBytes, if > 0, bounds how much is released this call (rounded down to the
// block size); pass <= 0 to release the whole trailing free run. path must name a
// database currently open in THIS process — Trim works through the live container,
// so it returns an error if none is open at path; use [Compact] on a closed file
// instead.
//
// The truncation is not separately fsynced, so the reclaim is best-effort across a
// crash: a crash right after Trim may leave the file at its pre-trim length. That
// is harmless — reopen rebuilds the same free list and the tail is reclaimable
// again — so Trim never corrupts, it only may not stick until the next commit.
func Trim(path string, maxBytes int64) (int64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	containers.mu.Lock()
	c := containers.m[abs]
	if c != nil {
		c.refs++ // pin the container so a concurrent last-handle Close cannot close the backing mid-trim
	}
	containers.mu.Unlock()
	if c == nil {
		return 0, fmt.Errorf("vault: Trim: no open database at %q (Trim needs the live container; run Compact on a closed file)", path)
	}
	defer func() { _ = c.release() }()
	return c.trim(maxBytes)
}

// trim returns a run of trailing free blocks to the OS, shrinking the physical
// file, and reports how many bytes were reclaimed. Only blocks that are BOTH free
// and at the very tail of the file (above every live slot, the directory, and the
// keyslot) can be truncated, so it reclaims space when the workload has left free
// blocks at the tail and reclaims nothing when the tail is occupied.
//
// The free list holds only DURABLY freed blocks — extents enter it after the
// commit that vacated them is durable — so truncating its tail run is exactly as
// safe as the allocator handing those same blocks to a new write: the
// authoritative superblock never references them, and a crash reopens at a file
// length whose rebuilt high-water mark excludes them. A write in flight leaves the
// tail occupied, so a concurrent transaction simply makes trim a no-op rather than
// a hazard (c.mu serializes it against I/O and commit).
func (c *container) trim(maxBytes int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readOnly {
		return 0, errReadOnly
	}
	if c.readOnlyRecipient {
		return 0, ErrReadOnlyRecipient // a reader with no write authority must not shrink the file
	}
	return c.trimLocked(maxBytes)
}

// trimLocked is the body of [container.trim] with the caller already holding c.mu
// and having checked the read-only guards. The online compactor calls it directly
// after a relocation commit, inside the same locked section.
func (c *container) trimLocked(maxBytes int64) (int64, error) {
	a := c.alloc
	n := len(a.free)
	if n == 0 {
		return 0, nil
	}
	last := a.free[n-1]
	if last.start+last.count != a.highWater {
		return 0, nil // the tail is occupied by a live extent; nothing to trim
	}
	release := last.count
	if maxBytes > 0 {
		if lim := uint64(maxBytes) / c.blockSize; lim < release {
			release = lim
		}
	}
	if release == 0 {
		return 0, nil
	}
	newSize := int64((a.highWater - release) * c.blockSize)
	if err := c.back.Truncate(newSize); err != nil {
		return 0, err // allocator untouched: the blocks stay free and reusable
	}
	a.highWater -= release
	if release == last.count {
		a.free = a.free[:n-1]
	} else {
		a.free[n-1].count -= release
	}
	return int64(release * c.blockSize), nil
}
