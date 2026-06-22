package compress

// Increment-1 unit tests for the on-disk container format and the block
// allocator. No SQLite, no codec: pure encode/decode and bookkeeping, where
// the crash-critical correctness lives. See format.go.

import (
	"encoding/binary"
	"hash/crc32"
	"math"
	"reflect"
	"testing"
)

func sampleSuperblock() *superblock {
	return &superblock{
		blockSize:   defaultBlockSize,
		pageSize:    defaultPageSize,
		pageCount:   12345,
		dirOffset:   2 * defaultBlockSize,
		dirBlocks:   9,
		generation:  7,
		codec:       1,
		enc:         0,
		dirChecksum: 0xCAFEF00D,
	}
}

func TestSuperblockRoundTrip(t *testing.T) {
	want := sampleSuperblock()
	got, err := parseSuperblock(want.marshal())
	if err != nil {
		t.Fatalf("parseSuperblock: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch:\n want %+v\n got  %+v", want, got)
	}
}

// TestSuperblockKeyslot covers the keyslot-offset field: it round-trips through
// marshal/parse, and validate bounds it (block-aligned, inside the file).
func TestSuperblockKeyslot(t *testing.T) {
	const bs = defaultBlockSize
	got, err := parseSuperblock((&superblock{
		blockSize: bs, pageSize: 65536, generation: 1, codec: 1,
		enc: encAdiantum, keyslotOffset: 5 * bs,
	}).marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got.enc != encAdiantum || got.keyslotOffset != 5*bs {
		t.Fatalf("keyslot/enc round-trip = %+v", got)
	}

	fileSize := int64(8 * bs)
	if err := got.validate(fileSize); err != nil {
		t.Fatalf("validate in-bounds keyslot: %v", err)
	}
	got.keyslotOffset = 100 * bs // past EOF
	if err := got.validate(fileSize); err == nil {
		t.Error("validate out-of-bounds keyslot: want error")
	}
	got.keyslotOffset = 5*bs + 1 // not block-aligned
	if err := got.validate(fileSize); err == nil {
		t.Error("validate unaligned keyslot: want error")
	}
}

func TestSuperblockChecksumRejection(t *testing.T) {
	b := sampleSuperblock().marshal()
	b[30] ^= 0xFF // flip a byte inside the checksummed region
	if _, err := parseSuperblock(b); err != errBadChecksum {
		t.Fatalf("corrupt superblock: got %v, want errBadChecksum", err)
	}
}

func TestSuperblockBadMagic(t *testing.T) {
	b := sampleSuperblock().marshal()
	b[0] = 'X'
	if _, err := parseSuperblock(b); err != errBadMagic {
		t.Fatalf("bad magic: got %v, want errBadMagic", err)
	}
}

func TestSuperblockBadVersion(t *testing.T) {
	b := sampleSuperblock().marshal()
	binary.LittleEndian.PutUint16(b[8:10], containerVersion+1)
	// rewrite the CRC so the version check, not the checksum, is what fires
	binary.LittleEndian.PutUint32(b[sbCRCOff:], crc32Checksum(b[:sbCRCOff]))
	if _, err := parseSuperblock(b); err != errBadVersion {
		t.Fatalf("bad version: got %v, want errBadVersion", err)
	}
}

func TestSuperblockShortBuffer(t *testing.T) {
	if _, err := parseSuperblock(make([]byte, superblockSize-1)); err != errBadChecksum {
		t.Fatalf("short buffer: got %v, want errBadChecksum", err)
	}
}

func TestPickSuperblockHighestGeneration(t *testing.T) {
	a := sampleSuperblock()
	a.generation = 4
	b := sampleSuperblock()
	b.generation = 5
	b.pageCount = 999 // a distinguishing field

	got, err := pickSuperblock(a.marshal(), b.marshal())
	if err != nil {
		t.Fatalf("pickSuperblock: %v", err)
	}
	if got.generation != 5 || got.pageCount != 999 {
		t.Fatalf("picked gen=%d pageCount=%d, want the gen-5 copy", got.generation, got.pageCount)
	}

	// Order must not matter.
	got, err = pickSuperblock(b.marshal(), a.marshal())
	if err != nil || got.generation != 5 {
		t.Fatalf("reversed order: gen=%d err=%v, want gen 5", got.generation, err)
	}
}

func TestPickSuperblockOneCorrupt(t *testing.T) {
	good := sampleSuperblock()
	good.generation = 2
	bad := sampleSuperblock()
	bad.generation = 9 // higher gen, but we will corrupt it
	corrupt := bad.marshal()
	corrupt[12] ^= 0xFF

	// Even though the corrupt copy has the higher generation, the valid copy wins.
	got, err := pickSuperblock(good.marshal(), corrupt)
	if err != nil || got.generation != 2 {
		t.Fatalf("one corrupt: gen=%d err=%v, want gen 2", got.generation, err)
	}
	got, err = pickSuperblock(corrupt, good.marshal())
	if err != nil || got.generation != 2 {
		t.Fatalf("one corrupt (reversed): gen=%d err=%v, want gen 2", got.generation, err)
	}
}

func TestPickSuperblockBothCorrupt(t *testing.T) {
	a := sampleSuperblock().marshal()
	b := sampleSuperblock().marshal()
	a[20] ^= 0xFF
	b[21] ^= 0xFF
	if _, err := pickSuperblock(a, b); err != errNoSuperblock {
		t.Fatalf("both corrupt: got %v, want errNoSuperblock", err)
	}
}

func TestDirectoryRoundTrip(t *testing.T) {
	want := []dirEntry{
		{}, // sparse page
		{physOffset: 2 * defaultBlockSize, storedLen: 4001, blocks: 1, flags: 0, checksum: 0xDEADBEEF},
		{physOffset: 3 * defaultBlockSize, storedLen: 70000, blocks: 18, flags: dirFlagVerbatim, checksum: 0x12345678},
	}
	for _, auth := range []bool{false, true} {
		got, err := parseDirectory(marshalDirectory(want, auth), len(want), auth)
		if err != nil {
			t.Fatalf("auth=%v parseDirectory: %v", auth, err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("auth=%v directory round-trip mismatch:\n want %+v\n got  %+v", auth, want, got)
		}
	}
}

func TestDirectoryShortBuffer(t *testing.T) {
	b := marshalDirectory(make([]dirEntry, 3), false)
	if _, err := parseDirectory(b[:len(b)-1], 3, false); err != errShortBlock {
		t.Fatalf("short directory: got %v, want errShortBlock", err)
	}
}

func TestBlocksFor(t *testing.T) {
	for _, tc := range []struct{ n, want uint64 }{
		{0, 0}, {1, 1}, {4096, 1}, {4097, 2}, {8192, 2}, {8193, 3},
	} {
		if got := blocksFor(tc.n, defaultBlockSize); got != tc.want {
			t.Errorf("blocksFor(%d) = %d, want %d", tc.n, got, tc.want)
		}
	}
}

func TestAllocatorCarvesFreeListFirst(t *testing.T) {
	a := newAllocator([]extent{{start: 10, count: 4}}, 100)

	// Partial carve: the head of the extent is handed out, the tail remains.
	if got := a.alloc(1); got != 10 {
		t.Fatalf("alloc(1) = %d, want 10 (head of free extent)", got)
	}
	if len(a.free) != 1 || a.free[0] != (extent{start: 11, count: 3}) {
		t.Fatalf("after partial carve free=%v, want [{11 3}]", a.free)
	}
	if a.highWater != 100 {
		t.Fatalf("highWater moved to %d on a free-list carve, want 100", a.highWater)
	}

	// Exact carve drops the extent entirely.
	if got := a.alloc(3); got != 11 {
		t.Fatalf("alloc(3) = %d, want 11", got)
	}
	if len(a.free) != 0 {
		t.Fatalf("after exact carve free=%v, want empty", a.free)
	}
}

func TestAllocatorGrowsWhenNoFitsFit(t *testing.T) {
	a := newAllocator([]extent{{start: 10, count: 2}}, 50)
	// Request larger than any free extent: must grow at the tail, free list intact.
	if got := a.alloc(8); got != 50 {
		t.Fatalf("alloc(8) = %d, want 50 (grow)", got)
	}
	if a.highWater != 58 {
		t.Fatalf("highWater = %d, want 58", a.highWater)
	}
	if len(a.free) != 1 || a.free[0] != (extent{start: 10, count: 2}) {
		t.Fatalf("free list disturbed: %v, want [{10 2}]", a.free)
	}
}

func TestAllocatorReleaseCoalesces(t *testing.T) {
	a := newAllocator(nil, 0)

	// Three separate frees that should NOT touch coalesce into one run.
	a.release(20, 2) // [20,22)
	a.release(10, 2) // [10,12)
	a.release(40, 2) // [40,42)
	if !reflect.DeepEqual(a.free, []extent{{10, 2}, {20, 2}, {40, 2}}) {
		t.Fatalf("disjoint frees = %v, want sorted [{10 2}{20 2}{40 2}]", a.free)
	}

	// Fill the gap [12,20): it should fuse [10,12)+[12,20)+[20,22) → [10,22).
	a.release(12, 8)
	if !reflect.DeepEqual(a.free, []extent{{10, 12}, {40, 2}}) {
		t.Fatalf("bridging free = %v, want [{10 12}{40 2}]", a.free)
	}

	// Total free blocks accounting.
	if got := a.freeBlocksTotal(); got != 14 {
		t.Fatalf("freeBlocksTotal = %d, want 14", got)
	}
}

func TestAllocatorReleaseThenReallocRoundTrips(t *testing.T) {
	a := newAllocator(nil, 2) // data region starts at block 2
	first := a.alloc(5)       // [2,7), grows highWater to 7
	second := a.alloc(3)      // [7,10), grows to 10
	if first != 2 || second != 7 || a.highWater != 10 {
		t.Fatalf("alloc sequence: first=%d second=%d hw=%d", first, second, a.highWater)
	}

	a.release(first, 5) // [2,7) goes back to the free list
	// A request that fits the freed hole must reuse it, not grow.
	if got := a.alloc(5); got != 2 {
		t.Fatalf("realloc = %d, want 2 (reused freed run)", got)
	}
	if a.highWater != 10 {
		t.Fatalf("highWater = %d after reuse, want 10 (no growth)", a.highWater)
	}
}

func TestNewAllocatorNormalisesInput(t *testing.T) {
	// Unsorted, adjacent input must come out sorted and coalesced.
	a := newAllocator([]extent{{start: 12, count: 3}, {start: 5, count: 7}}, 100)
	if !reflect.DeepEqual(a.free, []extent{{5, 10}}) {
		t.Fatalf("normalised free = %v, want [{5 10}] (sorted+coalesced)", a.free)
	}
}

func TestRebuildAllocatorFromDirectory(t *testing.T) {
	const bs = defaultBlockSize
	sb := &superblock{blockSize: bs, dirOffset: 2 * bs, dirBlocks: 1}
	dir := []dirEntry{
		{physOffset: 5 * bs, blocks: 2}, // page 0 → blocks 5,6
		{physOffset: 3 * bs, blocks: 1}, // page 1 → block 3
		{},                              // page 2 sparse → no blocks
	}
	// File spans 8 blocks. Used: dir{2,1}, page1{3,1}, page0{5,2}. Block 4 (a
	// gap) and block 7 (tail) are unreferenced → reclaimed as free.
	a := rebuildAllocator(dir, sb, 8*bs, 0)
	if !reflect.DeepEqual(a.free, []extent{{4, 1}, {7, 1}}) {
		t.Fatalf("rebuilt free = %v, want [{4 1}{7 1}]", a.free)
	}
	if a.highWater != 8 {
		t.Fatalf("rebuilt highWater = %d, want 8", a.highWater)
	}

	// Self-healing: an orphaned block (referenced by no committed entry) is
	// reclaimed automatically. Drop page 0 so blocks 5,6 are now orphaned.
	dir[0] = dirEntry{}
	a = rebuildAllocator(dir, sb, 8*bs, 0)
	if got := a.freeBlocksTotal(); got != 4 { // blocks 4,5,6,7
		t.Fatalf("after orphaning a slot, free total = %d, want 4", got)
	}
}

// crc32Checksum re-seals a hand-edited block with the package's superblock CRC
// so a test can isolate a non-checksum failure (e.g. a version mismatch).
func crc32Checksum(b []byte) uint32 { return crc32.Checksum(b, crc32C) }

func TestSuperblockValidateRejectsHostileFields(t *testing.T) {
	const fileSize = 1 << 20
	good := func() superblock {
		return superblock{blockSize: defaultBlockSize, pageSize: defaultPageSize, pageCount: 4, dirOffset: 2 * defaultBlockSize, dirBlocks: 1}
	}
	if err := (func() *superblock { s := good(); return &s }()).validate(fileSize); err != nil {
		t.Fatalf("valid superblock rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*superblock)
	}{
		{"zero block size", func(s *superblock) { s.blockSize = 0 }},
		{"zero page size", func(s *superblock) { s.pageSize = 0 }},
		{"non-pow2 block size", func(s *superblock) { s.blockSize = 4097 }},
		{"block size exceeds page size", func(s *superblock) { s.blockSize = 65536; s.pageSize = 4096 }},
		{"overflowing page count", func(s *superblock) { s.pageCount = math.MaxUint64 }},
		{"directory past EOF", func(s *superblock) { s.dirOffset = fileSize + 1 }},
		{"directory too small for pages", func(s *superblock) { s.pageCount = 1000; s.dirBlocks = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := good()
			tc.mutate(&s)
			if err := s.validate(fileSize); err == nil {
				t.Fatalf("validate accepted a hostile superblock (%s)", tc.name)
			}
		})
	}
}

func TestValidateDirectoryRejectsHostileEntries(t *testing.T) {
	const fileSize = 1 << 20
	sb := &superblock{blockSize: defaultBlockSize, pageSize: defaultPageSize}
	good := []dirEntry{
		{}, // sparse
		{physOffset: 2 * defaultBlockSize, storedLen: defaultBlockSize, blocks: 1, checksum: 1},
	}
	if err := validateDirectory(good, sb, fileSize); err != nil {
		t.Fatalf("valid directory rejected: %v", err)
	}
	for _, tc := range []struct {
		name  string
		entry dirEntry
	}{
		{"slot longer than a page", dirEntry{physOffset: 2 * defaultBlockSize, storedLen: defaultPageSize + 1, blocks: 17}},
		{"zero stored length", dirEntry{physOffset: 2 * defaultBlockSize, storedLen: 0, blocks: 0}},
		{"inconsistent block count", dirEntry{physOffset: 2 * defaultBlockSize, storedLen: defaultBlockSize, blocks: 99}},
		{"unaligned slot", dirEntry{physOffset: 2*defaultBlockSize + 1, storedLen: defaultBlockSize, blocks: 1}},
		{"slot past EOF", dirEntry{physOffset: fileSize, storedLen: defaultBlockSize, blocks: 1}},
		{"overflowing slot extent", dirEntry{physOffset: 0xFFFFFFFFFFFFF000, storedLen: defaultBlockSize, blocks: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateDirectory([]dirEntry{tc.entry}, sb, fileSize); err == nil {
				t.Fatalf("validateDirectory accepted a hostile entry (%s)", tc.name)
			}
		})
	}
}

// TestOpenRejectsMaliciousContainerNoPanic crafts superblocks with a valid CRC
// but hostile fields and confirms the open path returns a clean error rather
// than panicking — a panic would crash the host process through the CGo VFS
// callback. These are exactly the inputs that previously overflowed a slice
// bound, requested a multi-TiB allocation, or divided by zero.
func TestOpenRejectsMaliciousContainerNoPanic(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*superblock)
	}{
		{"zero block size", func(s *superblock) { s.blockSize = 0 }},
		{"overflowing page count", func(s *superblock) { s.pageCount = math.MaxUint64 }},
		{"huge directory extent", func(s *superblock) { s.dirOffset = 0; s.dirBlocks = 0xFFFFFFFF; s.pageCount = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sb := &superblock{blockSize: defaultBlockSize, pageSize: defaultPageSize, generation: 1}
			tc.mutate(sb)
			if _, err := openMainOver(newCrashBacking(sb.marshal()), false, defaultBlockSize, defaultPageSize, CompressionDefault); err == nil {
				t.Fatalf("opened a malicious container (%s) without error", tc.name)
			}
		})
	}
}
