package fts_test

import (
	"context"
	"testing"

	"github.com/go-again/sqlite/fts"
)

// TestContentless_RowidsOnly creates an FTS5 table in contentless mode
// (`content=”`), inserts text, queries for term matches, and confirms the
// rowids come back correctly. The Value field on Hit is NULL since the
// index doesn't store the original text — that's the cost of contentless.
//
// SQLite >= 3.43 also supports contentless_delete=1, exercised by the
// follow-up TestContentless_DeleteSupported test.
func TestContentless_RowidsOnly(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	idx, err := fts.New[int64, string](ctx, db, "log_fts", fts.Options{
		Columns:     []string{"msg"},
		Contentless: true,
	})
	if err != nil {
		t.Fatalf("New contentless: %v", err)
	}

	if err := idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 100, Value: "the quick brown fox"},
		fts.Attr[int64, string]{Key: 200, Value: "lazy dogs sleep at noon"},
		fts.Attr[int64, string]{Key: 300, Value: "the moon hangs over the river"},
	); err != nil {
		t.Fatal(err)
	}

	matches, err := idx.SearchSlice(ctx, fts.Term("moon"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Key != 300 {
		t.Errorf("Term('moon') matches=%+v, want [{Key:300}]", matches)
	}
	// Value is empty because the indexed text isn't stored.
	if matches[0].Value != "" {
		t.Errorf("contentless Value=%q, want empty (text not stored)", matches[0].Value)
	}
}

// TestContentless_DeleteSupported asserts contentless+ContentlessDelete=1
// allows DELETE to actually remove the indexed entry.
//
// Without ContentlessDelete=1 (the FTS5 default for contentless tables),
// DELETE would error with "cannot DELETE from contentless fts5 table". The
// option requires SQLite >= 3.43; modernc/sqlite ships a newer version so
// the test is unconditional.
func TestContentless_DeleteSupported(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	idx, err := fts.New[int64, string](ctx, db, "log_fts", fts.Options{
		Columns:           []string{"msg"},
		Contentless:       true,
		ContentlessDelete: true,
	})
	if err != nil {
		t.Fatalf("New contentless+delete: %v", err)
	}

	if err := idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "alpha"},
		fts.Attr[int64, string]{Key: 2, Value: "beta"},
	); err != nil {
		t.Fatal(err)
	}
	if err := idx.Delete(ctx, 1); err != nil {
		t.Fatalf("Delete on contentless table: %v", err)
	}
	matches, err := idx.SearchSlice(ctx, fts.Term("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("after delete, Term('alpha') should return no matches; got %+v", matches)
	}
	// "beta" survives.
	matches, err = idx.SearchSlice(ctx, fts.Term("beta"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Key != 2 {
		t.Errorf("'beta' matches=%+v, want [{Key:2}]", matches)
	}
}

// TestExternal_ContentTable verifies the external-content / contentless mode
// of FTS5. We create a regular table, an FTS5 index referencing it via the
// content= option, populate the source, then Rebuild() to populate the index.
//
// FTS5 docs say external-content indexes don't auto-sync on source updates,
// so explicit Rebuild is part of the workflow.
//
// See https://www.sqlite.org/fts5.html section 4.4.
func TestExternal_ContentTable(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
CREATE TABLE articles (
    id    INTEGER PRIMARY KEY,
    title TEXT
);
INSERT INTO articles (id, title) VALUES (1, 'fox and dog'), (2, 'cat alone');
`); err != nil {
		t.Fatal(err)
	}

	idx, err := fts.New[int64, string](ctx, db, "articles_fts", fts.Options{
		Columns:  []string{"title"},
		External: &fts.External{ContentTable: "articles", ContentRowid: "id"},
	})
	if err != nil {
		t.Fatalf("New external: %v", err)
	}

	// External-content tables don't auto-populate; Rebuild fills the index
	// from the source.
	if err := idx.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	matches, err := idx.SearchSlice(ctx, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Key != 1 {
		t.Errorf("Term('fox') matches=%+v, want [{Key:1}]", matches)
	}
}
