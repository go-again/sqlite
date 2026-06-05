package vec_test

import (
	"context"
	"testing"

	"github.com/go-again/sqlite/vec"
)

// TestKNN_KCapUpperBound_NoOOM pins round-4 K2: vec.KNNSlice with a
// k above the (max(k,0), 1024) pre-alloc clamp must still work and
// must NOT request a gigabyte slice up front. The actual k goes to
// sqlite-vec verbatim — sqlite-vec itself caps k at 4096 server-side
// — but our Go-side `out := make([]Neighbor, 0, capHint)` must use
// the clamp so a caller passing k=4000 doesn't burn 4000 * sizeof(
// Neighbor) immediately when the real result set is tiny.
func TestKNN_KCapUpperBound_NoOOM(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "items", 3, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 5; i++ {
		if err := tbl.Insert(ctx, i, []float32{float32(i), 0, 0}); err != nil {
			t.Fatal(err)
		}
	}
	// k well above the 1024 pre-alloc cap but inside sqlite-vec's own
	// 4096 server-side ceiling. Without our cap, the Go side would
	// request a slice of 4000 Neighbors before SQLite produces a single
	// row.
	hits, err := tbl.KNNSlice(ctx, []float32{0, 0, 0}, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 5 {
		t.Errorf("hits=%d, want 5 (all rows since k > N)", len(hits))
	}
}
