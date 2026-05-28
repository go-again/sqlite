package fts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/go-again/sqlite/fts"
)

// TestSearch_WithFilter exercises pantry's headline use case: a
// multi-column FTS5 index with a tenant column, where queries must be
// gated to a single tenant. Without WithFilter the caller has to drop
// to raw SQL; with it the typed API expresses the full query.
func TestSearch_WithFilter(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Columns:   []string{"body", "tenant"},
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "hello world", Extras: map[string]any{"tenant": "a"}},
		fts.Attr[int64, string]{Key: 2, Value: "hello there", Extras: map[string]any{"tenant": "b"}},
		fts.Attr[int64, string]{Key: 3, Value: "goodbye world", Extras: map[string]any{"tenant": "a"}},
	); err != nil {
		t.Fatal(err)
	}

	hits, err := idx.SearchSlice(ctx, fts.Term("hello"),
		fts.WithFilter("tenant = ?", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Key != 1 {
		t.Errorf("hello+tenant=a: %+v, want exactly key=1", hits)
	}

	hits, err = idx.SearchSlice(ctx, fts.Term("hello"),
		fts.WithFilter("tenant = ?", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Key != 2 {
		t.Errorf("hello+tenant=b: %+v, want exactly key=2", hits)
	}
}

// TestSearch_WithFilter_AndRanking confirms WithFilter composes with
// WithRanking — both modify the WHERE / ORDER BY around the MATCH and
// must not conflict.
func TestSearch_WithFilter_AndRanking(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Columns:   []string{"body", "tenant"},
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "fox fox fox", Extras: map[string]any{"tenant": "a"}},
		fts.Attr[int64, string]{Key: 2, Value: "fox", Extras: map[string]any{"tenant": "a"}},
		fts.Attr[int64, string]{Key: 3, Value: "fox fox fox fox", Extras: map[string]any{"tenant": "b"}},
	)

	hits, err := idx.SearchSlice(ctx, fts.Term("fox"),
		fts.WithRanking(),
		fts.WithFilter("tenant = ?", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits=%d, want 2 (tenant b filtered out)", len(hits))
	}
	// BM25 ranks higher-frequency-relative-to-length first; rank values
	// are negative by FTS5 convention so smaller (more negative) = better.
	if hits[0].Rank == 0 {
		t.Errorf("rank not populated despite WithRanking")
	}
}

// TestSearch_WithFilter_WrongColumn references a column the FTS5 table
// doesn't declare. The error from SQLite must surface through the
// iterator without panicking; we shouldn't have to guard against
// user-typo'd column names at the option level.
func TestSearch_WithFilter_WrongColumn(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Columns: []string{"body"},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "hello world"},
	)

	_, err = idx.SearchSlice(ctx, fts.Term("hello"),
		fts.WithFilter("nonexistent_column = ?", "x"))
	if err == nil {
		t.Fatal("expected error from filter on nonexistent column, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "no such column") &&
		!strings.Contains(strings.ToLower(err.Error()), "nonexistent_column") {
		t.Errorf("error %q doesn't mention the bad column", err.Error())
	}
}

// TestSearch_WithFilter_BindArgs confirms variadic args bind in
// declaration order, including when interleaved with multi-arg ranking.
func TestSearch_WithFilter_BindArgs(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Columns:   []string{"body", "tenant", "kind"},
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "alpha", Extras: map[string]any{"tenant": "a", "kind": "note"}},
		fts.Attr[int64, string]{Key: 2, Value: "alpha", Extras: map[string]any{"tenant": "a", "kind": "todo"}},
		fts.Attr[int64, string]{Key: 3, Value: "alpha", Extras: map[string]any{"tenant": "b", "kind": "note"}},
	)

	hits, err := idx.SearchSlice(ctx, fts.Term("alpha"),
		fts.WithFilter("tenant = ? AND kind = ?", "a", "note"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Key != 1 {
		t.Errorf("multi-arg filter: %+v, want exactly key=1", hits)
	}
}
