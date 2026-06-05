package fts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/go-again/sqlite/fts"
)

// TestWithFilter_ArgsAreBoundNotInterpolated pins the round-4 K3 trust
// model: WithFilter's args are bound via `?` placeholders, never
// `Sprintf`'d into the SQL. Without binding, the apostrophe-and-DROP
// payload below would close the literal and execute as DDL — instead
// it must be stored verbatim as a column value, then matched by exact
// equality.
//
// The test seeds a row with a tenant string that contains SQL
// metacharacters, then queries with the same string as a WithFilter
// argument. If the API were interpolating, SQLite would parse the
// payload's apostrophes and the comment marker, producing a syntax
// error or an empty result. Binding produces exactly the one row.
func TestWithFilter_ArgsAreBoundNotInterpolated(t *testing.T) {
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

	payload := "abc'; DROP TABLE docs --"
	if err := idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "hello", Extras: map[string]any{"tenant": payload}},
		fts.Attr[int64, string]{Key: 2, Value: "hello", Extras: map[string]any{"tenant": "innocent"}},
	); err != nil {
		t.Fatal(err)
	}

	hits, err := idx.SearchSlice(ctx, fts.Term("hello"),
		fts.WithFilter("tenant = ?", payload))
	if err != nil {
		t.Fatalf("WithFilter Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Key != 1 {
		t.Fatalf("got %v, want exactly key=1 with the payload tenant", hits)
	}

	// Sanity: the table still exists. If interpolation had happened,
	// the DROP TABLE would have fired on the previous query.
	if _, err := db.ExecContext(ctx, `SELECT 1 FROM docs LIMIT 1`); err != nil &&
		!strings.Contains(err.Error(), "external content") {
		t.Errorf("docs table appears to have been dropped: %v", err)
	}
}
