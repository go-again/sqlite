package vault

// compact_logical_online.go is the O(live) ONLINE reclaim: while the database stays
// open, it learns which inner-db pages are dead by reading SQLite's freelist, drops
// their container slots (no page is written, moved, or re-encrypted), and returns the
// freed blocks to the OS by relocating the holes down and trimming. Unlike
// incremental_vacuum it never re-ciphers a page (which would happen because the page
// cipher's tweak is the page number), and unlike CompactOnline alone it can shrink a
// big delete that was never vacuumed — because it, not SQLite, decides a page is free.
//
// Reading the freelist from the committed CONTAINER is safe even if the -wal still has
// un-checkpointed frames: every page on the container's freelist is either still free
// (its slot reads back as zeros, which is correct — SQLite never reads free-page
// content as data) or was re-allocated in the -wal (then it has a -wal frame that
// shadows the dropped container slot on read and re-materialises it on the next
// checkpoint). A re-allocated page always has a -wal frame, so a "free-in-container,
// live, no -wal frame" page cannot exist. So the operation preserves logical content
// regardless of -wal state; a committed generation (!c.dirty) is the only requirement.
// The caller should still checkpoint first for COMPLETENESS (a current freelist) and
// hold its write mutex for quiescence.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
)

// CompactLogicalOnline reclaims a large deletion from the OPEN container at path in
// O(live data) while the database stays open: it reads SQLite's freelist, drops the
// dead pages' container slots, and relocates + trims the freed blocks back to the OS.
// It does NOT re-encrypt or move live pages' content, and needs no prior
// incremental_vacuum or VACUUM. It reports the bytes returned to the filesystem.
//
// Call it on a quiescent container: checkpoint first (PRAGMA wal_checkpoint, so the
// committed freelist is in the container rather than the -wal) and make sure no write
// is in flight (the operation errors if the container is mid-transaction). It takes the
// container write-lock itself, so it is internally atomic against a concurrent
// CompactOnline/checkpoint, and it is crash-safe (dropped blocks are released only
// after the durable superblock flip). path must name a database open in THIS process;
// on a closed file use [CompactLogical].
func CompactLogicalOnline(path string) (int64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}
	containers.mu.Lock()
	c := containers.m[abs]
	if c != nil {
		c.refs++ // pin against a concurrent last-handle Close
	}
	containers.mu.Unlock()
	if c == nil {
		return 0, fmt.Errorf("vault: CompactLogicalOnline: no open database at %q (it needs the live container; run CompactLogical on a closed file)", path)
	}
	defer func() { _ = c.release() }()
	return c.compactLogicalOnline()
}

func (c *container) compactLogicalOnline() (reclaimed int64, err error) {
	c.mu.Lock()
	if c.readOnly {
		c.mu.Unlock()
		return 0, errReadOnly
	}
	if c.readOnlyRecipient {
		c.mu.Unlock()
		return 0, ErrReadOnlyRecipient
	}
	if c.dirty {
		// A committed generation is required: reading the freelist (and committing the
		// drops) over a half-written transaction would fold it in. Fail loudly so a
		// missed checkpoint is obvious rather than silently dropping live pages.
		c.mu.Unlock()
		return 0, errors.New("vault: CompactLogicalOnline needs a quiescent container; checkpoint and quiesce writes first")
	}

	leaves, perr := c.collectFreelistLeaves()
	if perr != nil {
		c.mu.Unlock()
		return 0, perr
	}
	dropped := 0
	for _, leaf := range leaves {
		idx := leaf - 1 // 1-based SQLite page number → 0-based directory index
		if idx < uint64(len(c.dir)) && c.dir[idx].physOffset != 0 {
			c.releaseLater(c.dir[idx].physOffset, c.dir[idx].blocks)
			c.dir[idx] = dirEntry{} // dead page → sparse slot; its block frees at the durable commit
			c.markSegmentDirty(idx)
			dropped++
		}
	}
	if dropped > 0 {
		c.dirty = true
		if cerr := c.commit(); cerr != nil {
			c.mu.Unlock()
			return 0, cerr
		}
		rec, terr := c.trimLocked(0) // return any freed tail run immediately
		if terr != nil {
			c.mu.Unlock()
			return 0, terr
		}
		reclaimed += rec
	}
	c.mu.Unlock()

	// Relocate the scattered middle holes the drops left down into the live region and
	// trim — the same crash-safe machinery CompactOnline uses.
	rec2, cerr := c.compactOnline(0, nil)
	reclaimed += rec2
	return reclaimed, cerr
}

// collectFreelistLeaves reads the committed inner-database freelist and returns the
// 1-based page numbers of its LEAF pages — the dead pages whose container slots can be
// dropped. The structural trunk pages are kept (SQLite reads them to walk the freelist
// when allocating). It is defensive against a corrupt or hostile freelist: every page
// number is bounded to [2, pageCount], the per-trunk leaf count is capped to the page,
// and the trunk walk is bounded by the header's freelist count with a visited-set cycle
// guard. Caller holds c.mu. The SQLite file format read here — page-1 header offsets 32
// (first trunk page) and 36 (freelist page count), and the trunk-page layout (next
// trunk at 0, leaf count at 4, then big-endian leaf page numbers) — is the entire
// format coupling, kept in this one function.
func (c *container) collectFreelistLeaves() ([]uint64, error) {
	if c.pageCount == 0 {
		return nil, nil
	}
	hdr := make([]byte, c.pageSize)
	if err := c.loadPageInto(hdr, 0); err != nil { // page 1: the database header
		return nil, fmt.Errorf("vault: read page 1 for freelist: %w", err)
	}
	firstTrunk := uint64(binary.BigEndian.Uint32(hdr[32:36]))
	freeCount := uint64(binary.BigEndian.Uint32(hdr[36:40]))
	if firstTrunk == 0 || freeCount == 0 {
		return nil, nil
	}
	maxLeavesPerTrunk := c.pageSize/4 - 2 // 8-byte trunk header, then 4-byte leaf pointers
	leaves := make([]uint64, 0, freeCount)
	seen := make(map[uint64]struct{})
	page := make([]byte, c.pageSize)
	for trunk := firstTrunk; trunk != 0; {
		if trunk < 2 || trunk > c.pageCount {
			break // out of range: stop rather than read garbage
		}
		if _, dup := seen[trunk]; dup || uint64(len(seen)) >= freeCount {
			break // cycle, or more trunks than the header claims free pages: corrupt
		}
		seen[trunk] = struct{}{}
		if err := c.loadPageInto(page, trunk-1); err != nil {
			return nil, fmt.Errorf("vault: read freelist trunk %d: %w", trunk, err)
		}
		next := uint64(binary.BigEndian.Uint32(page[0:4]))
		n := uint64(binary.BigEndian.Uint32(page[4:8]))
		if n > maxLeavesPerTrunk {
			n = maxLeavesPerTrunk
		}
		for i := uint64(0); i < n; i++ {
			leaf := uint64(binary.BigEndian.Uint32(page[8+i*4 : 12+i*4]))
			if leaf >= 2 && leaf <= c.pageCount {
				leaves = append(leaves, leaf)
			}
		}
		trunk = next
	}
	return leaves, nil
}
