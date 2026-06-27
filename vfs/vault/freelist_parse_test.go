package vault

import (
	"encoding/binary"
	"slices"
	"testing"
)

// TestCollectFreelistLeavesHostile drives collectFreelistLeaves — the only code that
// parses the UNTRUSTED on-disk SQLite freelist — against crafted page-1 + trunk-page
// bytes, proving its guards (firstTrunk/leaf range clamp, per-trunk leaf-count cap,
// and the visited-set + freeCount cycle bound) hold: it must never panic, never loop
// forever, and never return a page number outside [2, pageCount].
func TestCollectFreelistLeavesHostile(t *testing.T) {
	const ps = crashPageSize

	// A plaintext container with nPages real (non-sparse) pages; the caller then
	// overwrites page 1 and chosen trunk pages with crafted bytes. Leaf PAGES are
	// never read by the parser (only their numbers), so their content is irrelevant.
	newC := func(nPages int) (*container, *mainFile) {
		ct, err := newContainerOver(newCrashBacking(nil), false, defaultBlockSize, ps, 4, CompressionNone, keyConfig{})
		if err != nil {
			t.Fatal(err)
		}
		f := &mainFile{c: ct}
		for p := range nPages {
			if _, err := f.WriteAt(constPage(byte(p+1), ps), int64(p)*ps); err != nil {
				t.Fatal(err)
			}
		}
		return ct, f
	}
	writePage := func(f *mainFile, idx0 int, b []byte) {
		if _, err := f.WriteAt(b, int64(idx0)*ps); err != nil {
			t.Fatal(err)
		}
	}
	// SQLite page 1 (the db header): firstTrunk at byte 32, freelist count at byte 36.
	hdrPage := func(firstTrunk, freeCount uint32) []byte {
		b := make([]byte, ps)
		b[0] = 0xff // keep it non-sparse
		binary.BigEndian.PutUint32(b[32:36], firstTrunk)
		binary.BigEndian.PutUint32(b[36:40], freeCount)
		return b
	}
	// A freelist trunk page: next trunk at byte 0, leaf count at byte 4, then 4-byte
	// big-endian leaf page numbers.
	trunkPage := func(next, leafCount uint32, leaves ...uint32) []byte {
		b := make([]byte, ps)
		binary.BigEndian.PutUint32(b[0:4], next)
		binary.BigEndian.PutUint32(b[4:8], leafCount)
		for i, lf := range leaves {
			binary.BigEndian.PutUint32(b[8+i*4:12+i*4], lf)
		}
		b[ps-1] = 1 // ensure non-sparse even when next/leafCount are 0
		return b
	}
	inRange := func(t *testing.T, leaves []uint64, pageCount uint64) {
		for _, l := range leaves {
			if l < 2 || l > pageCount {
				t.Fatalf("returned out-of-range leaf %d (pageCount %d)", l, pageCount)
			}
		}
	}

	// A self-referential trunk + a wildly inflated freeCount must terminate via the
	// visited-set guard, not spin.
	t.Run("self_cycle_inflated_count", func(t *testing.T) {
		ct, f := newC(8)
		writePage(f, 0, hdrPage(2, 1_000_000))
		writePage(f, 1, trunkPage(2, 1, 5)) // page2.next = page2 (self-cycle), leaf 5
		got, err := ct.collectFreelistLeaves()
		if err != nil {
			t.Fatal(err)
		}
		inRange(t, got, ct.pageCount)
		if !slices.Equal(got, []uint64{5}) {
			t.Fatalf("leaves = %v, want [5]", got)
		}
	})

	// firstTrunk past pageCount must stop immediately with nothing dropped.
	t.Run("out_of_range_first_trunk", func(t *testing.T) {
		ct, f := newC(8)
		writePage(f, 0, hdrPage(99, 5))
		got, err := ct.collectFreelistLeaves()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("out-of-range firstTrunk returned %v, want none", got)
		}
	})

	// A leaf count of 0xFFFFFFFF must be capped to what fits the page (no OOB slice),
	// and a cross-trunk cycle (page2->page3->page2) must terminate.
	t.Run("oversized_leaf_count_and_cross_cycle", func(t *testing.T) {
		ct, f := newC(8)
		writePage(f, 0, hdrPage(2, 1_000_000))
		writePage(f, 1, trunkPage(3, 0xFFFFFFFF)) // page2: huge (capped) leaf count, no leaves set
		writePage(f, 2, trunkPage(2, 1, 4))       // page3 -> page2 (cycle), leaf 4
		got, err := ct.collectFreelistLeaves()
		if err != nil {
			t.Fatal(err)
		}
		inRange(t, got, ct.pageCount)
		if !slices.Equal(got, []uint64{4}) {
			t.Fatalf("leaves = %v, want [4]", got)
		}
	})

	// Leaf numbers that are page 1, zero, or beyond pageCount are filtered.
	t.Run("out_of_range_leaves_filtered", func(t *testing.T) {
		ct, f := newC(8)
		writePage(f, 0, hdrPage(2, 1))
		writePage(f, 1, trunkPage(0, 4, 1, 9999, 0, 6)) // page1, >pageCount, 0, then valid 6
		got, err := ct.collectFreelistLeaves()
		if err != nil {
			t.Fatal(err)
		}
		inRange(t, got, ct.pageCount)
		if !slices.Equal(got, []uint64{6}) {
			t.Fatalf("leaves = %v, want [6]", got)
		}
	})
}
