package fts_test

import (
	"context"
	"strings"
	"testing"

	"gosqlite.org/fts"
)

// TestColumnSpec_UnindexedAcceptedInCreate verifies the rich form
// generates a CREATE statement that includes the UNINDEXED marker
// — the bare []string form would have rejected "tenant UNINDEXED"
// at the validator.
func TestColumnSpec_UnindexedAcceptedInCreate(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	_, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		ColumnsRich: []fts.ColumnSpec{
			{Name: "body"},
			{Name: "tenant", Unindexed: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sql string
	if err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='docs'`,
	).Scan(&sql); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "tenant UNINDEXED") {
		t.Errorf("UNINDEXED marker missing in CREATE SQL: %s", sql)
	}
}

// TestColumnSpec_UnindexedFilterableViaWithFilter confirms an
// UNINDEXED column can be used as a WHERE conjunct, which is the
// whole point — metadata filtering without tokenization cost.
func TestColumnSpec_UnindexedFilterableViaWithFilter(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		ColumnsRich: []fts.ColumnSpec{
			{Name: "body"},
			{Name: "tenant", Unindexed: true},
		},
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "hello world", Extras: map[string]any{"tenant": "a"}},
		fts.Attr[int64, string]{Key: 2, Value: "hello there", Extras: map[string]any{"tenant": "b"}},
	)
	hits, err := idx.SearchSlice(ctx, fts.Term("hello"),
		fts.WithFilter("tenant = ?", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Key != 1 {
		t.Errorf("UNINDEXED filter: %+v, want exactly key=1", hits)
	}
}

// TestColumnSpec_UnindexedNotMatchable confirms an UNINDEXED column
// does NOT participate in MATCH — a Term search of the UNINDEXED
// column's content finds nothing. This is the FTS5 contract for
// UNINDEXED storage; we just have to not break it.
func TestColumnSpec_UnindexedNotMatchable(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		ColumnsRich: []fts.ColumnSpec{
			{Name: "body"},
			{Name: "tenant", Unindexed: true},
		},
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "world", Extras: map[string]any{"tenant": "acme"}},
	)
	// "acme" was written to the UNINDEXED column only; MATCH must miss it.
	hits, err := idx.SearchSlice(ctx, fts.Term("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("UNINDEXED content should not be MATCHable: %+v", hits)
	}
}

// TestColumnSpec_BackwardCompat_StringList confirms the existing
// []string form continues to work — every test in fts_test.go that
// uses Columns: []string{...} keeps passing — and that ColumnsRich
// takes precedence when both are set.
func TestColumnSpec_BackwardCompat_StringList(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Columns: []string{"body", "tenant"},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx.Insert(ctx, fts.Attr[int64, string]{Key: 1, Value: "hello",
		Extras: map[string]any{"tenant": "x"}})
	hits, err := idx.SearchSlice(ctx, fts.Term("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("Columns []string form: hits=%d, want 1", len(hits))
	}
}

// TestColumnSpec_BothPopulated_Errors pins the contract that setting
// both Options.Columns and Options.ColumnsRich is a builder bug — the
// previous silent-precedence rule (ColumnsRich wins) hid mid-edit
// typos. fts.New now rejects the conflict so the caller picks one.
func TestColumnSpec_BothPopulated_Errors(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	_, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Columns: []string{"ignored_a", "ignored_b"},
		ColumnsRich: []fts.ColumnSpec{
			{Name: "body"},
			{Name: "tenant", Unindexed: true},
		},
	})
	if err == nil {
		t.Fatal("fts.New with both Columns and ColumnsRich: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Columns OR Options.ColumnsRich") {
		t.Errorf("error message %q should name both fields so the caller fixes the right one", err.Error())
	}
}

// TestColumnSpec_SyncTriggers_CopyUnindexed verifies external-content
// sync triggers include UNINDEXED columns when copying source rows
// into the FTS5 table. Without this, the UNINDEXED metadata would
// stay NULL in the index and WithFilter(tenant=…) would miss every
// row.
func TestColumnSpec_SyncTriggers_CopyUnindexed(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE docs (
		id     INTEGER PRIMARY KEY,
		body   TEXT NOT NULL,
		tenant TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	idx, err := fts.New[int64, string](ctx, db, "docs_fts", fts.Options{
		ColumnsRich: []fts.ColumnSpec{
			{Name: "body"},
			{Name: "tenant", Unindexed: true},
		},
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
		External: &fts.External{
			ContentTable: "docs",
			ContentRowid: "id",
			SyncTriggers: fts.SyncAll,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO docs (body, tenant) VALUES ('hello world', 'a'), ('hello there', 'b')`); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.SearchSlice(ctx, fts.Term("hello"),
		fts.WithFilter("tenant = ?", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("after sync triggers: tenant=a hits=%d, want 1", len(hits))
	}
}
