package vault

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
	superblockMagic = "VAULTv01" // exactly 8 bytes

	// containerVersion is bumped on any incompatible wire-format change. Version 2
	// introduced the segmented page directory: the superblock points at a small
	// segment index, and the directory lives in fixed-size segments rewritten
	// independently, so a commit's directory write is O(changed pages).
	containerVersion = 2

	// defaultBlockSize is the physical block granularity B: every physical
	// read and write is a multiple of it. 4 KiB matches common device sectors
	// and leaves room for Phase 2 per-block encryption.
	defaultBlockSize = 4096

	// defaultPageSize is the logical SQLite page size. A large page amortises
	// the per-page directory entry and widens the compression window.
	defaultPageSize = 65536

	// defaultSegEntries is the number of page-directory entries per on-disk
	// directory segment. A commit re-marshals and re-encrypts only the segments
	// whose pages changed (plus the small segment index), so this trades the index
	// size against the per-dirty-segment write cost. Stored in the superblock
	// (self-describing), so a container always reads back at whatever value it was
	// created with.
	defaultSegEntries = 1024

	// superblockBlocks is the reserved prefix: block 0 = superblock A,
	// block 1 = superblock B. The data region begins at this block index.
	superblockBlocks = 2

	// superblockSize is the fixed encoded length of a superblock: base fields, a
	// 1-byte authenticated flag, the authenticated-mode fields (a directory hash +
	// writer signature, left zero when not authenticated), and a trailing CRC over
	// everything before it. One fixed layout with one CRC — a torn write anywhere
	// fails that CRC, so the ping-pong falls back to the prior generation. It is
	// padded out by the block write to occupy block 0/1 alone.
	superblockSize = sbCRCOff + 4 // 164

	dirHashLen   = 32 // crypto hash of the on-disk directory (authenticated mode)
	writerSigLen = 64 // ed25519 signature over the signed prefix (authenticated mode)

	// Superblock byte offsets. marshal and parse must agree byte-for-byte, so the
	// interior offsets are named once here rather than spelled as literals.
	sbAuthOff       = 60                            // 1-byte authenticated flag (0/1)
	sbSegEntriesOff = 61                            // segEntries (u32): directory entries per segment
	sbDirHashOff    = sbSegEntriesOff + 4           // 65: dirHash[dirHashLen] (hashes the segment index)
	sbWriterSigOff  = sbDirHashOff + dirHashLen     // 97: writerSig[writerSigLen]
	sbSignedLen     = sbWriterSigOff                // 97: bytes a writer signs (base ‖ auth flag ‖ segEntries ‖ dirHash)
	sbCRCOff        = sbWriterSigOff + writerSigLen // 161: CRC32C over [0:sbCRCOff]

	// dirEntrySize is the encoded length of one page-directory entry;
	// dirEntrySizeAuth adds a 16-byte per-slot crypto hash in authenticated mode.
	dirEntrySize     = 24
	slotHashLen      = 16
	dirEntrySizeAuth = dirEntrySize + slotHashLen
)

// dirEntryBytes is the on-disk directory-entry size for the container's mode.
func dirEntryBytes(authenticated bool) int {
	if authenticated {
		return dirEntrySizeAuth
	}
	return dirEntrySize
}

// crc32C is the Castagnoli table shared by the superblock, the directory and
// every per-slot checksum.
var crc32C = crc32.MakeTable(crc32.Castagnoli)

var (
	errBadMagic     = errors.New("vault: bad superblock magic")
	errBadChecksum  = errors.New("vault: superblock checksum mismatch")
	errBadVersion   = errors.New("vault: unsupported container version")
	errShortBlock   = errors.New("vault: block too short to decode")
	errNoSuperblock = errors.New("vault: no valid superblock (not a compressed container)")
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
// commit path.
type superblock struct {
	blockSize     uint32 // physical block size B
	pageSize      uint32 // logical SQLite page size
	pageCount     uint64 // logical page count; logical size = pageSize*pageCount
	dirOffset     uint64 // physical byte offset of the segment-index extent (0 ⇒ empty directory)
	dirBlocks     uint32 // segment-index length in blocks (0 ⇒ no pages yet)
	segEntries    uint32 // directory entries per segment (geometry; see defaultSegEntries)
	generation    uint64 // monotonic; newest valid superblock wins
	codec         uint8  // 0 raw / 1 az
	enc           uint8  // page-cipher kind (0 = unencrypted); see crypt.go
	dirChecksum   uint32 // CRC32C of the directory content bytes (0 ⇒ empty directory)
	keyslotOffset uint64 // physical byte offset of the keyslot block (0 ⇒ none)

	// Authenticated mode (authenticated ⇒ writer-signed container). dirHash is a
	// crypto hash of the on-disk directory bytes; writerSig is an ed25519 signature
	// over the signed prefix (base ‖ auth flag ‖ dirHash) by an authorized writer.
	authenticated bool
	dirHash       [dirHashLen]byte
	writerSig     [writerSigLen]byte
}

// marshal encodes s into a fresh superblockSize-byte block, terminating it with a
// CRC32C over every preceding byte. The authenticated fields are zero when the
// container is not authenticated; the single CRC covers them either way.
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
	binary.LittleEndian.PutUint64(b[52:60], s.keyslotOffset)
	binary.LittleEndian.PutUint32(b[sbSegEntriesOff:sbSegEntriesOff+4], s.segEntries)
	if s.authenticated {
		b[sbAuthOff] = 1
		copy(b[sbDirHashOff:], s.dirHash[:])
		copy(b[sbWriterSigOff:], s.writerSig[:])
	}
	binary.LittleEndian.PutUint32(b[sbCRCOff:], crc32.Checksum(b[:sbCRCOff], crc32C))
	return b
}

// signedState is the byte string a writer signs (and a reader verifies): the
// superblock prefix [0:sbSignedLen] — base fields, the authenticated flag, and the
// directory hash — binding the committed state (generation, page count, directory
// location + hash, cipher, keyslot) to the writer. It excludes the signature and
// CRC. Independent of writerSig, so it is valid before the signature is set.
func (s *superblock) signedState() []byte {
	return s.marshal()[:sbSignedLen]
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
	if got := crc32.Checksum(b[:sbCRCOff], crc32C); got != binary.LittleEndian.Uint32(b[sbCRCOff:]) {
		return nil, errBadChecksum
	}
	if v := binary.LittleEndian.Uint16(b[8:10]); v != containerVersion {
		return nil, errBadVersion
	}
	sb := &superblock{
		blockSize:     binary.LittleEndian.Uint32(b[10:14]),
		pageSize:      binary.LittleEndian.Uint32(b[14:18]),
		pageCount:     binary.LittleEndian.Uint64(b[18:26]),
		dirOffset:     binary.LittleEndian.Uint64(b[26:34]),
		dirBlocks:     binary.LittleEndian.Uint32(b[34:38]),
		generation:    binary.LittleEndian.Uint64(b[38:46]),
		codec:         b[46],
		enc:           b[47],
		dirChecksum:   binary.LittleEndian.Uint32(b[48:52]),
		keyslotOffset: binary.LittleEndian.Uint64(b[52:60]),
		segEntries:    binary.LittleEndian.Uint32(b[sbSegEntriesOff : sbSegEntriesOff+4]),
		authenticated: b[sbAuthOff] != 0,
	}
	if sb.authenticated {
		copy(sb.dirHash[:], b[sbDirHashOff:sbDirHashOff+dirHashLen])
		copy(sb.writerSig[:], b[sbWriterSigOff:sbWriterSigOff+writerSigLen])
	}
	return sb, nil
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
		return fmt.Errorf("vault: invalid container block size %d (want power of two in [512, 65536])", s.blockSize)
	}
	if !isPow2InRange(int(s.pageSize)) {
		return fmt.Errorf("vault: invalid container page size %d (want power of two in [512, 65536])", s.pageSize)
	}
	if s.blockSize > s.pageSize {
		return fmt.Errorf("vault: container block size %d exceeds page size %d", s.blockSize, s.pageSize)
	}
	if s.segEntries == 0 {
		return errors.New("vault: container segment size is zero")
	}
	// Bound pageCount so neither the logical size (pageCount*pageSize) nor the
	// directory length (pageCount*dirEntrySize) can overflow int64/uint64.
	if s.pageCount > uint64(math.MaxInt64)/uint64(s.pageSize) {
		return fmt.Errorf("vault: container page count %d too large for page size %d", s.pageCount, s.pageSize)
	}
	bs := uint64(s.blockSize)
	fsz := uint64(fileSize)
	dirBytes := uint64(s.dirBlocks) * bs // dirBlocks(u32) * blockSize(<=65536) cannot overflow
	if s.dirOffset > fsz || dirBytes > fsz-s.dirOffset {
		return fmt.Errorf("vault: segment index extent [%d,+%d) out of bounds (file %d bytes)", s.dirOffset, dirBytes, fsz)
	}
	// dirOffset/dirBlocks is the segment index: it must hold one descriptor per
	// segment (the encrypted form also has a canary, which only adds bytes).
	if nSegs := segmentCount(s.pageCount, uint64(s.segEntries)); nSegs*segDescSize > dirBytes {
		return fmt.Errorf("vault: segment index holds %d bytes, too small for %d segments", dirBytes, nSegs)
	}
	// The keyslot block (if present) must be a block-aligned extent inside the
	// file; its self-described length is bounded when the block is read.
	if s.keyslotOffset != 0 {
		if s.keyslotOffset%bs != 0 || s.keyslotOffset >= fsz || bs > fsz-s.keyslotOffset {
			return fmt.Errorf("vault: keyslot offset %d out of bounds (file %d bytes)", s.keyslotOffset, fsz)
		}
		// Defense in depth: the keyslot's first block must not overlap the
		// directory extent. A legitimate keyslot is allocated disjoint from the
		// directory; a crafted container that aliases them would otherwise have the
		// keyslot read interpret directory bytes as a wrapped key.
		if s.keyslotOffset < s.dirOffset+dirBytes && s.dirOffset < s.keyslotOffset+bs {
			return fmt.Errorf("vault: keyslot offset %d overlaps the directory extent [%d,+%d)", s.keyslotOffset, s.dirOffset, dirBytes)
		}
	}
	return nil
}

// validateDirectory rejects directory entries whose slot extents could overflow
// or fall outside the file — the per-page counterpart to [superblock.validate],
// so a crafted entry cannot drive a huge per-page allocation or an out-of-bounds
// read. It assumes sb already passed validate (blockSize/pageSize sane).
func validateDirectory(dir []dirEntry, segs []segDesc, sb *superblock, fileSize int64, keyslotBlocks uint32) error {
	bs := uint64(sb.blockSize)
	fsz := uint64(fileSize)
	for i, e := range dir {
		if e.physOffset == 0 {
			continue // sparse: storedLen/blocks are ignored on read
		}
		if e.storedLen == 0 || uint64(e.storedLen) > uint64(sb.pageSize) {
			return fmt.Errorf("vault: page %d slot length %d out of range (page size %d)", i, e.storedLen, sb.pageSize)
		}
		if uint64(e.blocks) != blocksFor(uint64(e.storedLen), bs) {
			return fmt.Errorf("vault: page %d block count %d inconsistent with slot length %d", i, e.blocks, e.storedLen)
		}
		if e.physOffset%bs != 0 {
			return fmt.Errorf("vault: page %d slot offset %d not block-aligned", i, e.physOffset)
		}
		span := uint64(e.blocks) * bs // blocks(u32) * blockSize(<=65536) cannot overflow
		if end := e.physOffset + span; end < e.physOffset || end > fsz {
			return fmt.Errorf("vault: page %d slot [%d,+%d) out of bounds (file %d bytes)", i, e.physOffset, span, fsz)
		}
	}

	// Reject overlapping extents. In a committed container the superblocks, the
	// directory, the keyslot, and every live slot are distinct allocations, so they
	// must be mutually disjoint — that is exactly the invariant rebuildAllocator
	// assumes. An overlap means a corrupt or (in the unauthenticated case)
	// re-sealed/tampered directory; left unchecked, rebuildAllocator folds the
	// overlap into one used run and can later hand out a block two pages still
	// reference, i.e. silent cross-page corruption. Authenticated mode catches this
	// via the signed directory; this is the structural guard for the rest.
	type blockRun struct{ start, end uint64 } // [start,end) in block units
	runs := make([]blockRun, 0, len(dir)+len(segs)+3)
	runs = append(runs, blockRun{0, superblockBlocks}) // the two reserved superblocks
	if sb.dirBlocks > 0 {
		s := sb.dirOffset / bs
		runs = append(runs, blockRun{s, s + uint64(sb.dirBlocks)}) // the segment-index extent
	}
	if sb.keyslotOffset != 0 {
		s := sb.keyslotOffset / bs
		n := uint64(keyslotBlocks) // the FULL keyslot extent (a large recipient set spans many blocks)
		if n == 0 {
			n = 1 // defensive: pin at least the first block if the count is unknown
		}
		runs = append(runs, blockRun{s, s + n})
	}
	for i, d := range segs { // each directory-segment extent (bounds, then overlap)
		if d.physOffset == 0 {
			continue
		}
		if d.physOffset%bs != 0 {
			return fmt.Errorf("vault: directory segment %d offset %d not block-aligned", i, d.physOffset)
		}
		span := uint64(d.blocks) * bs
		if end := d.physOffset + span; d.blocks == 0 || end < d.physOffset || end > fsz {
			return fmt.Errorf("vault: directory segment %d extent [%d,+%d) out of bounds (file %d bytes)", i, d.physOffset, span, fsz)
		}
		s := d.physOffset / bs
		runs = append(runs, blockRun{s, s + uint64(d.blocks)})
	}
	for _, e := range dir {
		if e.physOffset == 0 {
			continue
		}
		s := e.physOffset / bs
		runs = append(runs, blockRun{s, s + uint64(e.blocks)})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].start < runs[j].start })
	for i := 1; i < len(runs); i++ {
		if runs[i].start < runs[i-1].end {
			return fmt.Errorf("vault: overlapping extents in committed directory (block %d falls inside [%d,%d))", runs[i].start, runs[i-1].start, runs[i-1].end)
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
	checksum   uint32 // CRC32C of the on-disk slot bytes (corruption detection)
	// hash is a crypto hash of the on-disk slot bytes, present in authenticated
	// mode only. The directory is signed (via the superblock), so this binds each
	// slot's content cryptographically — a CRC32 alone is collision-prone and a
	// data-key holder could otherwise swap a slot for colliding bytes.
	hash [slotHashLen]byte
}

const dirFlagVerbatim uint16 = 1 << 0 // slot bytes are the raw page (did not shrink)

// marshalInto writes e into the first dirEntryBytes(auth) bytes of b.
func (e dirEntry) marshalInto(b []byte, auth bool) {
	binary.LittleEndian.PutUint64(b[0:8], e.physOffset)
	binary.LittleEndian.PutUint32(b[8:12], e.storedLen)
	binary.LittleEndian.PutUint32(b[12:16], e.blocks)
	binary.LittleEndian.PutUint16(b[16:18], e.flags)
	// b[18:20] reserved
	binary.LittleEndian.PutUint32(b[20:24], e.checksum)
	if auth {
		copy(b[24:24+slotHashLen], e.hash[:])
	}
}

// parseDirEntry decodes one entry from the first dirEntryBytes(auth) bytes of b.
func parseDirEntry(b []byte, auth bool) dirEntry {
	e := dirEntry{
		physOffset: binary.LittleEndian.Uint64(b[0:8]),
		storedLen:  binary.LittleEndian.Uint32(b[8:12]),
		blocks:     binary.LittleEndian.Uint32(b[12:16]),
		flags:      binary.LittleEndian.Uint16(b[16:18]),
		checksum:   binary.LittleEndian.Uint32(b[20:24]),
	}
	if auth {
		copy(e.hash[:], b[24:24+slotHashLen])
	}
	return e
}

// marshalDirectory encodes the whole directory as a dense array indexed by
// logical page number.
func marshalDirectory(entries []dirEntry, auth bool) []byte {
	sz := dirEntryBytes(auth)
	b := make([]byte, len(entries)*sz)
	for i := range entries {
		entries[i].marshalInto(b[i*sz:], auth)
	}
	return b
}

// parseDirectory decodes n entries from b (n is the superblock's pageCount).
func parseDirectory(b []byte, n int, auth bool) ([]dirEntry, error) {
	sz := dirEntryBytes(auth)
	if len(b) < n*sz {
		return nil, errShortBlock
	}
	entries := make([]dirEntry, n)
	for i := range entries {
		entries[i] = parseDirEntry(b[i*sz:], auth)
	}
	return entries, nil
}

// segDesc is one entry of the on-disk segment index: where a directory segment's
// block run lives and how to verify it. The index is itself a small extent the
// superblock points at and (in authenticated mode) hashes, so the integrity chain
// is superblock → segment index → segments → pages. A zero physOffset marks a
// segment that has never been written (all its pages are sparse).
type segDesc struct {
	physOffset uint64            // byte offset of the segment's block run; 0 ⇒ unwritten
	storedLen  uint32            // encoded segment length in bytes, before block padding
	blocks     uint32            // blocks the segment occupies
	checksum   uint32            // CRC32C of the on-disk segment bytes
	hash       [slotHashLen]byte // crypto hash of the on-disk segment bytes (authenticated mode)
}

// segDescSize is the encoded length of one segment-index entry.
const segDescSize = 8 + 4 + 4 + 4 + slotHashLen // 36

func (d segDesc) marshalInto(b []byte) {
	binary.LittleEndian.PutUint64(b[0:8], d.physOffset)
	binary.LittleEndian.PutUint32(b[8:12], d.storedLen)
	binary.LittleEndian.PutUint32(b[12:16], d.blocks)
	binary.LittleEndian.PutUint32(b[16:20], d.checksum)
	copy(b[20:20+slotHashLen], d.hash[:])
}

func parseSegDesc(b []byte) segDesc {
	d := segDesc{
		physOffset: binary.LittleEndian.Uint64(b[0:8]),
		storedLen:  binary.LittleEndian.Uint32(b[8:12]),
		blocks:     binary.LittleEndian.Uint32(b[12:16]),
		checksum:   binary.LittleEndian.Uint32(b[16:20]),
	}
	copy(d.hash[:], b[20:20+slotHashLen])
	return d
}

// marshalSegmentIndex encodes the segment index as a dense array of descriptors.
func marshalSegmentIndex(segs []segDesc) []byte {
	b := make([]byte, len(segs)*segDescSize)
	for i := range segs {
		segs[i].marshalInto(b[i*segDescSize:])
	}
	return b
}

// parseSegmentIndex decodes n segment descriptors from b.
func parseSegmentIndex(b []byte, n int) ([]segDesc, error) {
	if len(b) < n*segDescSize {
		return nil, errShortBlock
	}
	segs := make([]segDesc, n)
	for i := range segs {
		segs[i] = parseSegDesc(b[i*segDescSize:])
	}
	return segs, nil
}

// segmentCount is the number of directory segments for pageCount pages at
// segEntries entries per segment (the segment index length).
func segmentCount(pageCount, segEntries uint64) uint64 {
	if segEntries == 0 {
		return 0
	}
	return (pageCount + segEntries - 1) / segEntries
}

// segmentBounds returns the half-open page range [lo, hi) covered by segment s.
func segmentBounds(s, segEntries, pageCount uint64) (lo, hi uint64) {
	lo = s * segEntries
	return lo, min(lo+segEntries, pageCount)
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
		panic("vault: blocksFor with zero block size")
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
		panic("vault: alloc of zero blocks")
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

// lowestFitAbove returns the start block of the lowest-addressed free run of at
// least blocks contiguous blocks that starts at or above floor, and whether one
// exists. The online compactor uses it to relocate a slot strictly downward while
// leaving the lowest free blocks (below floor) reserved for the rewritten directory.
// It does not allocate.
func (a *allocator) lowestFitAbove(blocks, floor uint64) (uint64, bool) {
	for _, e := range a.free {
		start, count := e.start, e.count
		if start < floor { // clip the part of this extent below the reserve floor
			if start+count <= floor {
				continue
			}
			count -= floor - start
			start = floor
		}
		if count >= blocks {
			return start, true
		}
	}
	return 0, false
}

// allocAt reserves exactly [start, start+blocks) from the free list, which must lie
// entirely within one free extent (the compactor passes a run lowestFitAbove just
// returned). It carves that extent into the up-to-two fragments around the run,
// preserving the sorted, coalesced invariant.
func (a *allocator) allocAt(start, blocks uint64) {
	for i := range a.free {
		e := a.free[i]
		if start < e.start || start+blocks > e.start+e.count {
			continue
		}
		repl := append([]extent(nil), a.free[:i]...)
		if start > e.start {
			repl = append(repl, extent{start: e.start, count: start - e.start})
		}
		if end := start + blocks; end < e.start+e.count {
			repl = append(repl, extent{start: end, count: e.start + e.count - end})
		}
		a.free = append(repl, a.free[i+1:]...)
		return
	}
	panic("vault: allocAt run not within a free extent")
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
func rebuildAllocator(dir []dirEntry, segs []segDesc, sb *superblock, fileSize int64, keyslotBlocks uint32) *allocator {
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
	add(sb.dirOffset, sb.dirBlocks)      // the segment-index extent
	add(sb.keyslotOffset, keyslotBlocks) // referenced by the superblock, not the directory
	for _, d := range segs {             // each directory segment
		add(d.physOffset, d.blocks)
	}
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
