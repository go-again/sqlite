package vault

// compact_online.go is the online, in-place space reclaim: relocating live slots
// out of the middle and tail of the container into lower free holes while the
// database stays open, then trimming the freed tail. It is the online counterpart
// to the offline [Compact]: tail-only [Trim] cannot recover the scattered middle
// holes a delete leaves behind (after incremental_vacuum frees inner-db pages, the
// freed container slots sit between live ones), but relocation can, without
// unmounting.
//
// A slot's on-disk bytes are LOCATION-INDEPENDENT: the page cipher's tweak is the
// page number, not the physical offset, and the checksum and authentication hash
// are taken over those bytes — so a slot can be moved verbatim to a new offset with
// no re-encode, no re-encrypt, and a directory entry that only changes physOffset.
// Each batch of moves is a normal crash-safe copy-on-write commit (new location
// written first, old released only after the durable superblock flip), so a crash
// mid-compaction reopens at a consistent generation. The relocation is invisible to
// SQLite, which only ever addresses logical pages.

import (
	"fmt"
	"path/filepath"
	"sort"
)

// CompactOnline reclaims free space scattered through the middle of the OPEN
// container at path — space that tail-only [Trim] cannot reach — by relocating live
// slots down into the holes and truncating the freed tail, while the database stays
// open. It reports the bytes returned to the filesystem.
//
// It is the online answer to "a deleted large file does not shrink the mounted
// image": run the inner-database reclaim first (PRAGMA incremental_vacuum, then a
// checkpoint on a WAL database so the freed pages reach the container), then
// CompactOnline to return those freed container blocks to the OS. Offline [Compact]
// remains the densest reclaim (it also repacks the directory and defragments around
// stuck slots); CompactOnline trades a little density for not having to unmount.
//
// It works in bounded batches, each a crash-safe commit, releasing the container
// lock between them so the database keeps serving — so it is safe to call from a
// background goroutine. It makes progress only while the container is quiescent: if
// an uncommitted transaction is in flight it returns what it has reclaimed so far
// rather than folding that transaction into a relocation commit, so call it when
// writes are idle (e.g. right after the checkpoint). maxBytes, if > 0, caps how much
// is reclaimed this call; pass <= 0 to reclaim as much as possible. path must name a
// database currently open in THIS process; on a closed file use [Compact].
//
// progress, if non-nil, is called after each batch with the cumulative bytes
// reclaimed so far, so a long pass can report status and shrink visibly rather than
// appearing to change the file only at the end. It runs without the container lock
// held; keep it cheap and non-reentrant (do not call back into the same container).
func CompactOnline(path string, maxBytes int64, progress func(reclaimed int64)) (int64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	containers.mu.Lock()
	c := containers.m[abs]
	if c != nil {
		c.refs++ // pin so a concurrent last-handle Close cannot close the backing mid-compaction
	}
	containers.mu.Unlock()
	if c == nil {
		return 0, fmt.Errorf("vault: CompactOnline: no open database at %q (it needs the live container; run Compact on a closed file)", path)
	}
	defer func() { _ = c.release() }()
	return c.compactOnline(maxBytes, progress)
}

// relocateBatchBlocks bounds how many blocks one locked relocation batch moves, so
// the container lock is held only for a bounded slice before the database can run.
const relocateBatchBlocks = 4096

// compactOnline runs the relocate-commit-trim loop until no slot can move further
// down (everything is packed) or maxBytes is reached. Each iteration takes c.mu,
// relocates a batch, commits (crash-safe COW), trims the freed tail, and releases
// the lock.
func (c *container) compactOnline(maxBytes int64, progress func(int64)) (reclaimed int64, err error) {
	for {
		c.mu.Lock()
		if c.readOnly {
			c.mu.Unlock()
			return reclaimed, errReadOnly
		}
		if c.readOnlyRecipient {
			c.mu.Unlock()
			return reclaimed, ErrReadOnlyRecipient // a reader with no write authority must not rewrite the file
		}
		if c.dirty {
			// An uncommitted transaction is mid-flight; relocating + committing now would
			// fold it into a relocation commit. Stop and let the caller retry when idle.
			c.mu.Unlock()
			return reclaimed, nil
		}
		moved, rerr := c.relocateBatch(relocateBatchBlocks)
		if moved > 0 {
			if cerr := c.commit(); cerr != nil {
				c.mu.Unlock()
				return reclaimed, cerr
			}
			rec, terr := c.trimLocked(0)
			if terr != nil {
				c.mu.Unlock()
				return reclaimed, terr
			}
			reclaimed += rec
		}
		c.mu.Unlock()
		if moved > 0 && progress != nil {
			progress(reclaimed) // report cumulative reclaim after each batch (lock released)
		}
		if rerr != nil {
			return reclaimed, rerr // surface an I/O error, after committing the moves that did succeed
		}
		if moved == 0 {
			break // everything that can move down already has
		}
		if maxBytes > 0 && reclaimed >= maxBytes {
			break
		}
	}
	return reclaimed, nil
}

// relocateBatch moves up to maxBlocks blocks of live slots strictly downward into
// the lowest free holes that fit, highest-offset slots first, so the high-water mark
// can drop once the batch is committed and trimmed. A slot with no fitting hole
// below it is left in place. It returns the blocks moved and the first I/O error (if
// any); the caller commits whatever moved before surfacing the error. Caller holds
// c.mu (write); every move is recorded as copy-on-write (old run released at the
// next commit). Because each move is strictly downward, the sum of slot offsets
// strictly decreases, so repeated batches terminate.
func (c *container) relocateBatch(maxBlocks uint64) (uint64, error) {
	// Leave the lowest free blocks reserved for the directory the next commit
	// rewrites: if relocation filled every low hole, the directory would be forced to
	// the tail and block the trim. floor is the lowest block relocation may target.
	floor := c.dirReserveFloor()

	type cand struct {
		page, off uint64
		blocks    uint64
	}
	cands := make([]cand, 0, len(c.dir))
	for p := range c.dir {
		if e := c.dir[p]; e.physOffset != 0 {
			cands = append(cands, cand{uint64(p), e.physOffset, uint64(e.blocks)})
		}
	}
	// Highest physical offset first: relocating the topmost slots is what lets the
	// tail be trimmed after the commit.
	sort.Slice(cands, func(i, j int) bool { return cands[i].off > cands[j].off })

	var moved uint64
	for _, cd := range cands {
		if moved >= maxBlocks {
			break
		}
		start, ok := c.alloc.lowestFitAbove(cd.blocks, floor)
		if !ok || start*c.blockSize >= cd.off {
			continue // no free hole below this slot (above the reserve): leave it in place
		}
		c.alloc.allocAt(start, cd.blocks)
		if err := c.moveSlot(cd.page, start*c.blockSize); err != nil {
			c.alloc.release(start, cd.blocks) // undo the reservation; the slot stays put
			return moved, err
		}
		moved += cd.blocks
	}
	return moved, nil
}

// dirReserveFloor returns the lowest physical block that relocation may move a slot
// into, leaving the free blocks below it reserved for the directory the next commit
// rewrites (its segments plus the segment index). pageCount does not change across
// compaction, so the directory footprint is stable; a small margin covers index
// growth. If there is not enough low free space to hold the directory, it returns
// the high-water mark, so relocation moves nothing (the file is already tight).
func (c *container) dirReserveFloor() uint64 {
	var reserve uint64
	for _, d := range c.segIndex {
		reserve += uint64(d.blocks)
	}
	reserve += uint64(c.committedDirBlocks) + 2 // the index extent, plus a margin
	var acc uint64
	for _, e := range c.alloc.free {
		if acc+e.count >= reserve {
			return e.start + (reserve - acc)
		}
		acc += e.count
	}
	return c.alloc.highWater
}

// moveSlot relocates page pageNo's slot to newOff — a freshly allocated, lower block
// run of the same size — by copying its on-disk bytes verbatim (the ciphertext,
// checksum and authentication hash are location-independent, keyed by the page
// number, not the offset), repointing the directory entry, and scheduling the old
// run for release at the next durable commit (copy-on-write). The slot's
// storedLen/blocks/checksum/hash are unchanged, so the committed directory still
// verifies it. Caller holds c.mu (write).
func (c *container) moveSlot(pageNo, newOff uint64) error {
	e := c.dir[pageNo]
	span := uint64(e.blocks) * c.blockSize
	bp := getExtent(span)
	defer putExtent(bp)
	buf := *bp
	if _, err := c.back.ReadAt(buf, int64(e.physOffset)); err != nil {
		return fmt.Errorf("vault: relocate read page %d: %w", pageNo, err)
	}
	if _, err := c.back.WriteAt(buf, int64(newOff)); err != nil {
		return fmt.Errorf("vault: relocate write page %d: %w", pageNo, err)
	}
	c.releaseLater(e.physOffset, e.blocks) // old run freed only after the durable commit
	c.dir[pageNo].physOffset = newOff
	c.markSegmentDirty(pageNo)
	c.dirty = true
	return nil
}
