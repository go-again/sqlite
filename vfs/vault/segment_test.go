package vault

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"gosqlite.org/vfs/crypto"
)

// TestSegmentTamperAuthRejected closes the per-segment link of the integrity
// chain: in authenticated mode, tampering a byte of a directory SEGMENT (not the
// index) is rejected on reopen, because the segment's hash is pinned in the
// superblock-signed segment index.
func TestSegmentTamperAuthRejected(t *testing.T) {
	const ps = crashPageSize
	kc := keyConfig{cipher: crypto.Adiantum, rawKey: randKey(t), authenticate: true}
	cb := newCrashBacking(nil)
	ct, err := newContainerOver(cb, false, defaultBlockSize, ps, 4, CompressionNone, kc)
	if err != nil {
		t.Fatal(err)
	}
	f := &mainFile{c: ct}
	for p := range 12 { // 3 segments of 4 pages
		if _, err := f.WriteAt(constPage(byte(p+1), ps), int64(p)*ps); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Sync(0); err != nil {
		t.Fatal(err)
	}
	segOff := ct.segIndex[1].physOffset
	if segOff == 0 {
		t.Fatal("expected a written directory segment")
	}

	img := append([]byte(nil), cb.synced...)
	img[segOff] ^= 0xff // flip a byte inside segment 1 (the index stays valid)
	if _, err := newContainerOver(newCrashBacking(img), false, defaultBlockSize, ps, 4, CompressionNone, kc); !errors.Is(err, ErrTampered) {
		t.Fatalf("reopen of a tampered directory segment = %v, want ErrTampered", err)
	}
}

// TestSegmentedDirtyCommit proves the O(changed pages) win: after a one-page edit,
// the next commit rewrites ONLY the segment holding that page and carries the rest
// forward in place (their on-disk extents are unchanged), and the data round-trips.
func TestSegmentedDirtyCommit(t *testing.T) {
	const ps = crashPageSize
	cb := newCrashBacking(nil)
	f, err := openMainOver(cb, false, defaultBlockSize, ps, 4, CompressionNone) // 4 pages/segment
	if err != nil {
		t.Fatal(err)
	}
	c := f.c
	for p := range 20 { // 20 pages → 5 segments (0..4)
		if _, err := f.WriteAt(constPage(byte(p+1), ps), int64(p)*ps); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Sync(0); err != nil {
		t.Fatal(err)
	}
	if len(c.segIndex) != 5 {
		t.Fatalf("segments = %d, want 5", len(c.segIndex))
	}
	before := append([]segDesc(nil), c.segIndex...)

	// Edit one page in segment 2 (pages 8..11) and commit.
	if _, err := f.WriteAt(constPage(0xAA, ps), int64(9)*ps); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(0); err != nil {
		t.Fatal(err)
	}
	for s := range c.segIndex {
		changed := c.segIndex[s].physOffset != before[s].physOffset
		if want := s == 2; changed != want {
			t.Fatalf("segment %d rewritten=%v, want %v (only the dirtied segment rewrites)", s, changed, want)
		}
	}

	// Reopen over the durable image: the edited page and a carried-forward page
	// both read correctly.
	fr, err := openMainOver(newCrashBacking(cb.synced), false, defaultBlockSize, ps, 4, CompressionNone)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, ps)
	if _, err := fr.ReadAt(got, int64(9)*ps); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !bytes.Equal(got, constPage(0xAA, ps)) {
		t.Fatal("edited page 9 wrong after reopen")
	}
	if _, err := fr.ReadAt(got, int64(2)*ps); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !bytes.Equal(got, constPage(3, ps)) {
		t.Fatal("carried-forward page 2 wrong after reopen")
	}
}

// TestSegmentedTruncateRegrow proves a truncate that drops whole directory
// segments, followed by a regrow over a dropped segment within the SAME
// transaction, does not resurrect the truncated-away pages: the dropped segments
// are re-marked dirty so the commit recomputes them from the live directory
// instead of carrying the stale pre-truncate on-disk segment forward.
func TestSegmentedTruncateRegrow(t *testing.T) {
	const ps = crashPageSize
	cb := newCrashBacking(nil)
	f, err := openMainOver(cb, false, defaultBlockSize, ps, 4, CompressionNone) // 4 pages/segment
	if err != nil {
		t.Fatal(err)
	}
	for p := range 18 { // 18 pages → segments 0..4
		if _, err := f.WriteAt(constPage(byte(p+1), ps), int64(p)*ps); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.WriteAt(constPage(0xCC, ps), int64(10)*ps); err != nil { // page 10 ∈ segment 2
		t.Fatal(err)
	}
	if err := f.Sync(0); err != nil {
		t.Fatal(err)
	}

	// One transaction: truncate to 6 pages (drops segments 2,3,4), then write page 13
	// (regrows to 14 pages, bringing segment 2 back in range but all-sparse), commit.
	if err := f.Truncate(6 * ps); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(constPage(0xDD, ps), int64(13)*ps); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(0); err != nil {
		t.Fatal(err)
	}

	fr, err := openMainOver(newCrashBacking(cb.synced), false, defaultBlockSize, ps, 4, CompressionNone)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, ps)
	if _, err := fr.ReadAt(got, int64(10)*ps); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !bytes.Equal(got, make([]byte, ps)) {
		t.Fatalf("page 10 resurrected after truncate+regrow: first byte = %#x, want sparse/zero", got[0])
	}
	if _, err := fr.ReadAt(got, int64(13)*ps); err != nil && err != io.EOF { // the regrown page survives
		t.Fatal(err)
	}
	if !bytes.Equal(got, constPage(0xDD, ps)) {
		t.Fatal("regrown page 13 wrong after reopen")
	}
}

// TestSegmentIndexCodec round-trips the on-disk segment index and rejects a short
// buffer rather than panicking on hostile input.
func TestSegmentIndexCodec(t *testing.T) {
	segs := []segDesc{
		{physOffset: 4096, storedLen: 100, blocks: 1, checksum: 0xdeadbeef, hash: [slotHashLen]byte{1, 2, 3}},
		{physOffset: 0, storedLen: 0, blocks: 0}, // an unwritten (all-sparse) segment
		{physOffset: 8192, storedLen: 36, blocks: 1, checksum: 0xfeedface},
	}
	b := marshalSegmentIndex(segs)
	if len(b) != len(segs)*segDescSize {
		t.Fatalf("encoded length %d, want %d", len(b), len(segs)*segDescSize)
	}
	got, err := parseSegmentIndex(b, len(segs))
	if err != nil {
		t.Fatal(err)
	}
	for i := range segs {
		if got[i] != segs[i] {
			t.Fatalf("segment %d round-trip = %+v, want %+v", i, got[i], segs[i])
		}
	}
	if _, err := parseSegmentIndex(b[:len(b)-1], len(segs)); err == nil {
		t.Fatal("parseSegmentIndex of a short buffer: want an error, not a panic")
	}
}

// TestSegmentBounds checks the page-range arithmetic the commit/open paths use.
func TestSegmentBounds(t *testing.T) {
	// 2500 pages at 1024/segment → 3 segments: [0,1024) [1024,2048) [2048,2500).
	if n := segmentCount(2500, 1024); n != 3 {
		t.Fatalf("segmentCount(2500,1024) = %d, want 3", n)
	}
	if n := segmentCount(0, 1024); n != 0 {
		t.Fatalf("segmentCount(0,1024) = %d, want 0", n)
	}
	if n := segmentCount(1024, 1024); n != 1 {
		t.Fatalf("segmentCount(1024,1024) = %d, want 1", n)
	}
	for _, tc := range []struct {
		s, wantLo, wantHi uint64
	}{
		{0, 0, 1024},
		{1, 1024, 2048},
		{2, 2048, 2500}, // last, partial
	} {
		if lo, hi := segmentBounds(tc.s, 1024, 2500); lo != tc.wantLo || hi != tc.wantHi {
			t.Fatalf("segmentBounds(%d) = [%d,%d), want [%d,%d)", tc.s, lo, hi, tc.wantLo, tc.wantHi)
		}
	}
}
