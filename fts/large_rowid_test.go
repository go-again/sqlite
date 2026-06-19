package fts_test

import (
	"context"
	"testing"

	"gosqlite.org/fts"
)

// TestSearchPreservesInt64RowidAbove2_53 pins the contract that rowids
// above 2^53 — the largest exact integer representable as float64 —
// round-trip through Search without precision loss. Before the
// direct-int64 path in assignSQLType, the rowid was detoured through
// float64 and silently mangled, which broke FTS5 indexes keyed by
// snowflake IDs or unix-nano timestamps.
func TestSearchPreservesInt64RowidAbove2_53(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const bigRowid int64 = 1 << 60
	if err := idx.Insert(ctx, fts.Attr[int64, string]{Key: bigRowid, Value: "hello world"}); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.SearchSlice(ctx, fts.Term("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits=%d, want 1", len(hits))
	}
	if hits[0].Key != bigRowid {
		t.Errorf("rowid round-trip: got %d, want %d (delta %d)",
			hits[0].Key, bigRowid, hits[0].Key-bigRowid)
	}
}
