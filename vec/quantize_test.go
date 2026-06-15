package vec_test

import (
	"context"
	"testing"

	"github.com/go-again/sqlite/vec"
)

// TestTyped_Int8Encoding: an int8[N] column quantizes float vectors at the SQL
// layer (vec_quantize_int8 'unit'); a query that equals a stored vector quantizes
// to the same int8 and ranks that row first at ~zero distance.
func TestTyped_Int8Encoding(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{Encoding: vec.Int8})
	if err != nil {
		t.Fatalf("Create int8: %v", err)
	}
	if got := tbl.Encoding(); got != vec.Int8 {
		t.Errorf("Encoding() = %v, want Int8", got)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatalf("BatchInsert: %v", err)
	}

	// Query with rowid 2's own (in-range) vector: it quantizes identically, so
	// it must come back first with a near-zero distance.
	matches, err := tbl.KNNSlice(ctx, fixture[1].Embedding, 2)
	if err != nil {
		t.Fatalf("KNNSlice: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(matches))
	}
	if matches[0].Rowid != 2 {
		t.Errorf("nearest rowid = %d, want 2 (self-match)", matches[0].Rowid)
	}
	if matches[0].Distance > 1.0 {
		t.Errorf("self-match distance = %f, want ~0", matches[0].Distance)
	}
	// The runner-up distance must be on the int8 scale (squared int8 deltas →
	// tens/hundreds), not the float L2 scale (~2.x for unit vectors). This is
	// what makes the test int8-specific rather than passing for a float
	// passthrough.
	if len(matches) > 1 && matches[1].Distance < 10 {
		t.Errorf("non-self distance = %f, want int8-scale (>10); looks like a float passthrough", matches[1].Distance)
	}
}

// TestTyped_Int8OutOfRange pins the documented footgun: 'unit' quantization
// saturates out-of-[-1,1] components rather than erroring, so an over-range
// insert + query still self-matches at distance 0 (silently lossy).
func TestTyped_Int8OutOfRange(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{Encoding: vec.Int8})
	if err != nil {
		t.Fatal(err)
	}
	over := []float32{5, -5, 3, -3} // all outside [-1, 1]
	if err := tbl.Insert(ctx, 1, over); err != nil {
		t.Fatalf("over-range insert should succeed (saturating), got: %v", err)
	}
	matches, err := tbl.KNNSlice(ctx, over, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Rowid != 1 {
		t.Errorf("over-range self-match = %+v, want rowid 1", matches)
	}
}

// TestTyped_BitDimGuard / chunk-size guard: Create rejects an invalid bit dim or
// chunk_size up front with a clear typed error.
func TestTyped_BitDimGuard(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := vec.Create(ctx, db, "docs", 10, vec.Options{Encoding: vec.Bit}); err == nil {
		t.Error("bit[10] (not a multiple of 8) should be rejected at Create")
	}
	if _, err := vec.Create(ctx, db, "docs", 4, vec.Options{ChunkSize: 7}); err == nil {
		t.Error("ChunkSize=7 (not a multiple of 8) should be rejected at Create")
	}
	// The valid forms still succeed.
	if _, err := vec.Create(ctx, db, "bits", 8, vec.Options{Encoding: vec.Bit}); err != nil {
		t.Errorf("bit[8] should succeed: %v", err)
	}
}

// TestTyped_BitEncoding: a bit[N] column quantizes to one sign-bit per dimension
// and ranks by Hamming distance; Create forces the Hamming metric, and a query
// with the same sign pattern as a stored row matches it at distance 0.
func TestTyped_BitEncoding(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{Encoding: vec.Bit, Metric: vec.Cosine})
	if err != nil {
		t.Fatalf("Create bit: %v", err)
	}
	if got := tbl.Metric(); got != vec.Hamming {
		t.Errorf("Metric() = %v, want Hamming (forced for Bit even though Cosine was passed)", got)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatalf("BatchInsert: %v", err)
	}

	// Query with rowid 3's own vector: same sign pattern → identical bits →
	// Hamming distance 0, ranked first.
	matches, err := tbl.KNNSlice(ctx, fixture[2].Embedding, 4)
	if err != nil {
		t.Fatalf("KNNSlice: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no matches")
	}
	if matches[0].Rowid != 3 {
		t.Errorf("nearest rowid = %d, want 3 (self-match)", matches[0].Rowid)
	}
	if matches[0].Distance != 0 {
		t.Errorf("self-match Hamming distance = %f, want 0", matches[0].Distance)
	}
}

func TestParseEncodingMetric_Quantized(t *testing.T) {
	for in, want := range map[string]vec.Encoding{"int8": vec.Int8, "bit": vec.Bit} {
		if got, err := vec.ParseEncoding(in); err != nil || got != want {
			t.Errorf("ParseEncoding(%q) = %v, %v; want %v, nil", in, got, err, want)
		}
	}
	if got, err := vec.ParseMetric("hamming"); err != nil || got != vec.Hamming {
		t.Errorf("ParseMetric(hamming) = %v, %v; want Hamming, nil", got, err)
	}
}
