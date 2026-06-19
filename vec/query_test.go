package vec_test

import (
	"context"
	"testing"

	"gosqlite.org/vec"
)

// TestQuery_WithFilter_RestrictsToRowidSubset is the canonical filtered-KNN
// case: combine the MATCH-driven distance ordering with a regular WHERE
// clause to limit the search to a known rowid subset. Tenant-scoped vector
// search ("only this user's documents") works this way.
func TestQuery_WithFilter_RestrictsToRowidSubset(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}

	// Unfiltered baseline: top-2 hits are rowid 2 then rowid 1.
	base, err := tbl.KNNSlice(ctx, fixtureQuery, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 2 || base[0].Rowid != 2 || base[1].Rowid != 1 {
		t.Fatalf("baseline KNN unexpected: %+v", base)
	}

	// Restrict to rowids {3, 4} via WithFilter. Result should never include
	// rowids 1 or 2.
	filtered, err := tbl.KNNSlice(ctx, fixtureQuery, 4,
		vec.WithFilter("rowid IN (?, ?)", int64(3), int64(4)))
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("filtered KNN: got %d rows, want 2", len(filtered))
	}
	for _, m := range filtered {
		if m.Rowid != 3 && m.Rowid != 4 {
			t.Errorf("filtered KNN returned rowid=%d, want only {3,4}", m.Rowid)
		}
	}
}

// TestQuery_WithFilter_EmptyResult asserts that a filter excluding all rows
// returns no matches and does not error. Uses an IN-list form rather than
// a comparison; sqlite-vec's planner can downgrade certain comparison
// filters in ways that bypass the LIMIT requirement, so the IN form is the
// safer pattern to document.
func TestQuery_WithFilter_EmptyResult(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	matches, err := tbl.KNNSlice(ctx, fixtureQuery, 4,
		vec.WithFilter("rowid IN (?, ?)", int64(99999), int64(99998)))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("filter excluding all rows returned %+v, want empty", matches)
	}
}

// TestQuery_WithFilter_InvalidSQLSurfaces shows that malformed user SQL
// surfaces as a query error rather than being silently dropped.
func TestQuery_WithFilter_InvalidSQLSurfaces(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	_, err = tbl.KNNSlice(ctx, fixtureQuery, 1, vec.WithFilter("not a real clause"))
	if err == nil {
		t.Errorf("expected error from malformed WithFilter SQL")
	}
}
