package crypto

import "testing"

// TestScratchPool_ReturnsRequestedSize confirms getScratch always
// returns a slice whose length matches the request, regardless of
// the pool's current capacity.
func TestScratchPool_ReturnsRequestedSize(t *testing.T) {
	for _, n := range []int64{16, 4096, 65536, 1 << 20} {
		bp := getScratch(n)
		if int64(len(*bp)) != n {
			t.Errorf("getScratch(%d): len=%d, want %d", n, len(*bp), n)
		}
		putScratch(bp)
	}
}

// TestScratchPool_GrowPathDoesNotPanic exercises the B1 code path
// (pooled buffer too small for the request — getScratch must Put
// the small one back BEFORE allocating the larger one). The "no
// silent leak" property can't be directly observed against
// sync.Pool's non-deterministic Get/Put semantics; this test just
// pins that the grow path doesn't panic, deadlock, or return a
// short buffer. Correctness verified by code review of getScratch
// in iomethods.go.
func TestScratchPool_GrowPathDoesNotPanic(t *testing.T) {
	for range 100 {
		bp := getScratch(512)
		putScratch(bp)
		bp = getScratch(65536) // forces grow path most iterations
		if int64(len(*bp)) != 65536 {
			t.Fatalf("grow path len=%d, want 65536", len(*bp))
		}
		putScratch(bp)
	}
}

// TestScratchPool_PutResetsLength confirms putScratch zeros the
// slice length so subsequent getScratch callers can resize without
// dragging old element values through.
func TestScratchPool_PutResetsLength(t *testing.T) {
	buf := make([]byte, 100, 200)
	bp := &buf
	putScratch(bp)
	if len(*bp) != 0 {
		t.Errorf("putScratch left len=%d, want 0", len(*bp))
	}
}
