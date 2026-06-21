package compress

// format.go is the on-disk wire format of the live compressing VFS: the
// block-structured container that the public vfs.VFS in this package reads and
// writes. It holds no SQLite or codec contact — only encode/decode of
// the metadata structures and the block allocator that the I/O path builds on.
// Keeping it free-standing means the crash-critical bookkeeping is ordinary Go
// over []byte and can be unit-tested exhaustively before any database is wired
// up. The wire format and the commit protocol that drives it are described in
// .plans/plan-compress-vfs-phase1.md.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"sort"
)

const (
	// superblockMagic prefixes both superblock copies. It is deliberately not
	// the SQLite magic: SQLite never sees the physical file, only what ReadAt
	// synthesises, so the container is free to brand block 0 as its own.
	superblockMagic = "goSQLZv1" // exactly 8 bytes

	// containerVersion is bumped on any incompatible wire-format change.
	containerVersion = 1

	// defaultBlockSize is the physical block granularity B: every physical
	// read and write is a multiple of it. 4 KiB matches common device sectors
	// and leaves room for Phase 2 per-block encryption.
	defaultBlockSize = 4096

	// defaultPageSize is the logical SQLite page size. A large page amortises
	// the per-page directory entry and widens the compression window.
	defaultPageSize = 65536

	// superblockBlocks is the reserved prefix: block 0 = superblock A,
	// block 1 = superblock B. The data region begins at this block index.
	superblockBlocks = 2

	// superblockSize is the fixed encoded length of a superblock, padded out to
	// occupy block 0/1 alone. The encoded fields are far smaller; the rest of
	// the block is unused.
	superblockSize = 64

	// dirEntrySize is the fixed encoded length of one page-directory entry.
	dirEntrySize = 24
)

// crc32C is the Castagnoli table shared by the superblock, the directory and
// every per-slot checksum.
var crc32C = crc32.MakeTable(crc32.Castagnoli)

var (
	errBadMagic     = errors.New("compress: bad superblock magic")
	errBadChecksum  = errors.New("compress: superblock checksum mismatch")
	errBadVersion   = errors.New("compress: unsupported container version")
	errShortBlock   = errors.New("compress: block too short to decode")
	errNoSuperblock = errors.New("compress: no valid superblock (not a compressed container)")
)

// superblock is the root metadata block. Two copies live at physical blocks 0
// and 1; the authoritative one is the valid-checksum copy with the highest
// generation (ping-pong). The commit protocol writes the *alternate* copy and
// flips authority only after the write is durable, so a crash leaves the prior
// generation intact. Encoded little-endian with a trailing CRC32C.
//
// There is deliberately no on-disk free-map: the allocator is rebuilt by
// scanning the committed directory on open (see [rebuildAllocator]), which
// makes open self-healing and keeps the free list off the crash-critical
// commit path. Bytes [52:60] are reserved (zero) for a future format extension
// without a version bump.
type superblock struct {
	blockSize   uint32 // physical block size B
	pageSize    uint32 // logical SQLite page size
	pageCount   uint64 // logical page count; logical size = pageSize*pageCount
	dirOffset   uint64 // physical byte offset of the directory extent (0 ⇒ empty directory)
	dirBlocks   uint32 // directory length in blocks (0 ⇒ no pages yet)
	generation  uint64 // monotonic; newest valid superblock wins
	codec       uint8  // 0 raw / 1 az
	enc         uint8  // reserved for Phase 2 encryption
	dirChecksum uint32 // CRC32C of the directory content bytes (0 ⇒ empty directory)
}

// marshal encodes s into a fresh superblockSize-byte block, terminating it with
// a CRC32C over every preceding byte.
func (s *superblock) marshal() []byte {
	b := make([]byte, superblockSize)
	copy(b[0:8], superblockMagic)
	binary.LittleEndian.PutUint16(b[8:10], containerVersion)
	binary.LittleEndian.PutUint32(b[10:14], s.blockSize)
	binary.LittleEndian.PutUint32(b[14:18], s.pageSize)
	binary.LittleEndian.PutUint64(b[18:26], s.pageCount)
	binary.LittleEndian.PutUint64(b[26:34], s.dirOffset)
	binary.LittleEndian.PutUint32(b[34:38], s.dirBlocks)
	binary.LittleEndian.PutUint64(b[38:46], s.generation)
	b[46] = s.codec
	b[47] = s.enc
	binary.LittleEndian.PutUint32(b[48:52], s.dirChecksum)
	// b[52:60] reserved (zero)
	binary.LittleEndian.PutUint32(b[60:64], crc32.Checksum(b[:60], crc32C))
	return b
}

// parseSuperblock decodes one superblock copy, rejecting a wrong magic, a
// failed checksum, or an unknown version. A short buffer fails as a checksum
// error: it cannot carry a valid block.
func parseSuperblock(b []byte) (*superblock, error) {
	if len(b) < superblockSize {
		return nil, errBadChecksum
	}
	if string(b[0:8]) != superblockMagic {
		return nil, errBadMagic
	}
	if got := crc32.Checksum(b[:60], crc32C); got != binary.LittleEndian.Uint32(b[60:64]) {
		return nil, errBadChecksum
	}
	if v := binary.LittleEndian.Uint16(b[8:10]); v != containerVersion {
		return nil, errBadVersion
	}
	return &superblock{
		blockSize:   binary.LittleEndian.Uint32(b[10:14]),
		pageSize:    binary.LittleEndian.Uint32(b[14:18]),
		pageCount:   binary.LittleEndian.Uint64(b[18:26]),
		dirOffset:   binary.LittleEndian.Uint64(b[26:34]),
		dirBlocks:   binary.LittleEndian.Uint32(b[34:38]),
		generation:  binary.LittleEndian.Uint64(b[38:46]),
		codec:       b[46],
		enc:         b[47],
		dirChecksum: binary.LittleEndian.Uint32(b[48:52]),
	}, nil
}

// pickSuperblockSlot selects the authoritative superblock from the two on-disk
// copies — the valid-checksum copy with the highest generation — and reports
// which slot (0 or 1) it came from, so the caller knows the older slot to write
// next. It errors only when NEITHER copy is valid (a corrupt or non-container
// file).
func pickSuperblockSlot(a, b []byte) (sb *superblock, slot int, err error) {
	sa, ea := parseSuperblock(a)
	sbB, eb := parseSuperblock(b)
	switch {
	case ea == nil && eb == nil:
		if sa.generation >= sbB.generation {
			return sa, 0, nil
		}
		return sbB, 1, nil
	case ea == nil:
		return sa, 0, nil
	case eb == nil:
		return sbB, 1, nil
	default:
		return nil, -1, errNoSuperblock
	}
}

// pickSuperblock is pickSuperblockSlot without the slot — the authoritative
// superblock, or an error if neither copy is valid.
func pickSuperblock(a, b []byte) (*superblock, error) {
	sb, _, err := pickSuperblockSlot(a, b)
	return sb, err
}

// validate rejects a superblock whose fields could overflow allocation or
// offset math, or that names a directory extent that does not fit the file. The
// CRC only proves the bytes are self-consistent — an attacker who controls the
// container recomputes it for any chosen values — so the open path must bound
// the fields before using them, or a crafted file panics (slice overflow,
// divide-by-zero) or exhausts memory inside a VFS callback. fileSize is the
// physical backing length. All arithmetic is overflow-safe in uint64.
func (s *superblock) validate(fileSize int64) error {
	if !isPow2InRange(int(s.blockSize)) {
		return fmt.Errorf("compress: invalid container block size %d (want power of two in [512, 65536])", s.blockSize)
	}
	if !isPow2InRange(int(s.pageSize)) {
		return fmt.Errorf("compress: invalid container page size %d (want power of two in [512, 65536])", s.pageSize)
	}
	if s.blockSize > s.pageSize {
		return fmt.Errorf("compress: container block size %d exceeds page size %d", s.blockSize, s.pageSize)
	}
	// Bound pageCount so neither the logical size (pageCount*pageSize) nor the
	// directory length (pageCount*dirEntrySize) can overflow int64/uint64.
	if s.pageCount > uint64(math.MaxInt64)/uint64(s.pageSize) {
		return fmt.Errorf("compress: container page count %d too large for page size %d", s.pageCount, s.pageSize)
	}
	bs := uint64(s.blockSize)
	fsz := uint64(fileSize)
	dirBytes := uint64(s.dirBlocks) * bs // dirBlocks(u32) * blockSize(<=65536) cannot overflow
	if s.dirOffset > fsz || dirBytes > fsz-s.dirOffset {
		return fmt.Errorf("compress: directory extent [%d,+%d) out of bounds (file %d bytes)", s.dirOffset, dirBytes, fsz)
	}
	if s.pageCount*dirEntrySize > dirBytes {
		return fmt.Errorf("compress: directory holds %d bytes, too small for %d pages", dirBytes, s.pageCount)
	}
	return nil
}

// validateDirectory rejects directory entries whose slot extents could overflow
// or fall outside the file — the per-page counterpart to [superblock.validate],
// so a crafted entry cannot drive a huge per-page allocation or an out-of-bounds
// read. It assumes sb already passed validate (blockSize/pageSize sane).
func validateDirectory(dir []dirEntry, sb *superblock, fileSize int64) error {
	bs := uint64(sb.blockSize)
	fsz := uint64(fileSize)
	for i, e := range dir {
		if e.physOffset == 0 {
			continue // sparse: storedLen/blocks are ignored on read
		}
		if e.storedLen == 0 || uint64(e.storedLen) > uint64(sb.pageSize) {
			return fmt.Errorf("compress: page %d slot length %d out of range (page size %d)", i, e.storedLen, sb.pageSize)
		}
		if uint64(e.blocks) != blocksFor(uint64(e.storedLen), bs) {
			return fmt.Errorf("compress: page %d block count %d inconsistent with slot length %d", i, e.blocks, e.storedLen)
		}
		if e.physOffset%bs != 0 {
			return fmt.Errorf("compress: page %d slot offset %d not block-aligned", i, e.physOffset)
		}
		span := uint64(e.blocks) * bs // blocks(u32) * blockSize(<=65536) cannot overflow
		if end := e.physOffset + span; end < e.physOffset || end > fsz {
			return fmt.Errorf("compress: page %d slot [%d,+%d) out of bounds (file %d bytes)", i, e.physOffset, span, fsz)
		}
	}
	return nil
}

// dirEntry maps a logical page to its physical slot. A zero entry (physOffset
// == 0) is a sparse page that was never written; reads of it zero-fill. The
// data region starts past the superblocks, so offset 0 can never be a real
// slot and is unambiguous as "sparse".
type dirEntry struct {
	physOffset uint64 // physical byte offset of the slot; 0 ⇒ sparse
	storedLen  uint32 // stored (compressed) slot length in bytes; 0 ⇒ sparse
	blocks     uint32 // blocks the slot occupies (storedLen rounded up to B)
	flags      uint16 // bit0: slot stored verbatim (codec bypassed); rest reserved
	checksum   uint32 // CRC32C of the stored slot bytes
}

const dirFlagVerbatim uint16 = 1 << 0 // slot bytes are the raw page (did not shrink)

// marshalInto writes e into the first dirEntrySize bytes of b.
func (e dirEntry) marshalInto(b []byte) {
	binary.LittleEndian.PutUint64(b[0:8], e.physOffset)
	binary.LittleEndian.PutUint32(b[8:12], e.storedLen)
	binary.LittleEndian.PutUint32(b[12:16], e.blocks)
	binary.LittleEndian.PutUint16(b[16:18], e.flags)
	// b[18:20] reserved
	binary.LittleEndian.PutUint32(b[20:24], e.checksum)
}

// parseDirEntry decodes one entry from the first dirEntrySize bytes of b.
func parseDirEntry(b []byte) dirEntry {
	return dirEntry{
		physOffset: binary.LittleEndian.Uint64(b[0:8]),
		storedLen:  binary.LittleEndian.Uint32(b[8:12]),
		blocks:     binary.LittleEndian.Uint32(b[12:16]),
		flags:      binary.LittleEndian.Uint16(b[16:18]),
		checksum:   binary.LittleEndian.Uint32(b[20:24]),
	}
}

// marshalDirectory encodes the whole directory as a dense array indexed by
// logical page number.
func marshalDirectory(entries []dirEntry) []byte {
	b := make([]byte, len(entries)*dirEntrySize)
	for i := range entries {
		entries[i].marshalInto(b[i*dirEntrySize:])
	}
	return b
}

// parseDirectory decodes n entries from b (n is the superblock's pageCount).
func parseDirectory(b []byte, n int) ([]dirEntry, error) {
	if len(b) < n*dirEntrySize {
		return nil, errShortBlock
	}
	entries := make([]dirEntry, n)
	for i := range entries {
		entries[i] = parseDirEntry(b[i*dirEntrySize:])
	}
	return entries, nil
}

// extent is a run of contiguous physical blocks [start, start+count) measured
// in block indices, not bytes.
type extent struct {
	start uint64
	count uint64
}

// blocksFor returns the number of physical blocks needed to hold n bytes.
func blocksFor(n, blockSize uint64) uint64 {
	if blockSize == 0 {
		panic("compress: blocksFor with zero block size")
	}
	return (n + blockSize - 1) / blockSize
}

// allocator hands out block-aligned runs for slots, the directory and the
// free-map itself. It serves from a sorted, coalesced free list first
// (first-fit), then grows the backing region at the tail by bumping highWater.
// The free list is reconstructed from the committed directory on open
// (rebuildAllocator); highWater comes from the physical file size, so neither
// is stored separately.
//
// alloc/release/coalesce are O(n) in the number of free extents. That stays
// small because adjacent frees coalesce, so the list only grows under heavy
// fragmentation (many scattered, non-adjacent freed slots). A size-indexed
// structure would be worth it only if a fragmentation benchmark showed the list
// growing unbounded — not the case for the single-writer, large-page workload.
type allocator struct {
	free      []extent // sorted by start, non-adjacent (coalesced)
	highWater uint64   // first block past the allocated region
}

// newAllocator builds an allocator over a free list and a high-water mark. The
// free list is copied and normalised (sorted + coalesced) so callers can pass
// the raw parsed extents.
func newAllocator(free []extent, highWater uint64) *allocator {
	a := &allocator{free: append([]extent(nil), free...), highWater: highWater}
	sort.Slice(a.free, func(i, j int) bool { return a.free[i].start < a.free[j].start })
	a.coalesce()
	return a
}

// alloc reserves a run of blocks, returning the starting block index. It never
// fails: a free extent is carved first-fit, otherwise the region grows.
func (a *allocator) alloc(blocks uint64) uint64 {
	if blocks == 0 {
		panic("compress: alloc of zero blocks")
	}
	for i := range a.free {
		if a.free[i].count >= blocks {
			start := a.free[i].start
			if a.free[i].count == blocks {
				a.free = append(a.free[:i], a.free[i+1:]...)
			} else {
				a.free[i].start += blocks
				a.free[i].count -= blocks
			}
			return start
		}
	}
	start := a.highWater
	a.highWater += blocks
	return start
}

// release returns a run to the free list and coalesces it with neighbours. The
// commit protocol calls this only for extents superseded by a durable commit,
// so freed space is never reused before the generation that vacated it is safe.
func (a *allocator) release(start, count uint64) {
	if count == 0 {
		return
	}
	i := sort.Search(len(a.free), func(i int) bool { return a.free[i].start >= start })
	a.free = append(a.free, extent{})
	copy(a.free[i+1:], a.free[i:])
	a.free[i] = extent{start: start, count: count}
	a.coalesce()
}

// coalesce merges adjacent extents in the (already start-sorted) free list.
func (a *allocator) coalesce() {
	out := a.free[:0]
	for _, e := range a.free {
		if e.count == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].start+out[n-1].count == e.start {
			out[n-1].count += e.count
		} else {
			out = append(out, e)
		}
	}
	a.free = out
}

// freeBlocksTotal reports the total number of blocks currently on the free list.
func (a *allocator) freeBlocksTotal() uint64 {
	var n uint64
	for _, e := range a.free {
		n += e.count
	}
	return n
}

// rebuildAllocator reconstructs the block allocator for a just-opened container
// by scanning the committed directory: every block in [superblockBlocks,
// highWater) that is neither the directory extent nor a referenced slot is
// free. highWater is the physical file size in blocks (fileSize is the backing
// file's current length).
//
// Rebuilding from the durable directory — rather than persisting a free-map —
// makes open self-healing: any block a crash orphaned (a superseded slot or
// directory whose freeing never reached disk) is unreferenced by the committed
// directory, so it is reclaimed automatically. It also keeps the free list out
// of the crash-critical commit path entirely.
func rebuildAllocator(dir []dirEntry, sb *superblock, fileSize int64) *allocator {
	bs := uint64(sb.blockSize)
	highWater := uint64(fileSize) / bs

	type run struct{ start, count uint64 }
	var used []run
	add := func(physOffset uint64, blocks uint32) {
		if blocks == 0 {
			return
		}
		used = append(used, run{start: physOffset / bs, count: uint64(blocks)})
	}
	add(sb.dirOffset, sb.dirBlocks)
	for _, e := range dir {
		add(e.physOffset, e.blocks)
	}
	sort.Slice(used, func(i, j int) bool { return used[i].start < used[j].start })

	var free []extent
	cursor := uint64(superblockBlocks)
	for _, u := range used {
		if u.start > cursor {
			free = append(free, extent{start: cursor, count: u.start - cursor})
		}
		if end := u.start + u.count; end > cursor {
			cursor = end
		}
	}
	// A used run can extend past a non-block-aligned file length's floor; never
	// let the high-water mark fall inside a live run.
	if cursor > highWater {
		highWater = cursor
	}
	if highWater > cursor {
		free = append(free, extent{start: cursor, count: highWater - cursor})
	}
	return newAllocator(free, highWater)
}
