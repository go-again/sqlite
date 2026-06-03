package vec_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vec"
)

// openDB returns an in-memory DB pre-loaded with the sqlite-vec extension
// (via this package's import side-effect) for use as a typed-API test fixture.
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1) // virtual tables are per-conn; pin to one.
	return db
}

// fixture corresponds to the four vectors from sqlite-vec's documentation
// example. Reusing the same data across encoding + metric tests gives us a
// stable reference baseline.
var fixture = []vec.Row{
	{Rowid: 1, Embedding: []float32{-0.200, 0.250, 0.341, -0.211, 0.645, 0.935, -0.316, -0.924}},
	{Rowid: 2, Embedding: []float32{0.443, -0.501, 0.355, -0.771, 0.707, -0.708, -0.185, 0.362}},
	{Rowid: 3, Embedding: []float32{0.716, -0.927, 0.134, 0.052, -0.669, 0.793, -0.634, -0.162}},
	{Rowid: 4, Embedding: []float32{-0.710, 0.330, 0.656, 0.041, -0.990, 0.726, 0.385, -0.958}},
}
var fixtureQuery = []float32{0.890, 0.544, 0.825, 0.961, 0.358, 0.0196, 0.521, 0.175}

// TestTyped_CreateInsertKNN_JSON exercises the typed API end-to-end with the
// default JSON encoding and L2 metric, asserting the same rowid ordering as
// modernc's vec_test.go fixture.
func TestTyped_CreateInsertKNN_JSON(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}

	matches, err := tbl.KNNSlice(ctx, fixtureQuery, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(matches))
	}
	wantRowids := []int64{2, 1}
	wantDist := []float64{2.38687372207642, 2.38978505134583}
	for i, m := range matches {
		if m.Rowid != wantRowids[i] {
			t.Errorf("[%d] rowid=%d, want %d", i, m.Rowid, wantRowids[i])
		}
		if math.Abs(m.Distance-wantDist[i]) > 1e-6 {
			t.Errorf("[%d] distance=%f, want %f", i, m.Distance, wantDist[i])
		}
	}
}

// TestTyped_BinaryEncoding asserts that switching to binary encoding produces
// identical rankings (within float-precision tolerance) to JSON. Binary is the
// recommended encoding for bulk-insert performance.
func TestTyped_BinaryEncoding(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{Encoding: vec.Binary})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	matches, err := tbl.KNNSlice(ctx, fixtureQuery, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].Rowid != 2 || matches[1].Rowid != 1 {
		t.Errorf("binary KNN result %+v doesn't match JSON baseline", matches)
	}
}

// TestTyped_CosineMetric asserts that the Cosine metric is honored by the
// virtual table — same fixture, different metric, generally produces a
// different ranking. The exact rowid order isn't asserted (sqlite-vec's
// ranking is implementation-specific); we just check that distances are
// monotonically non-decreasing, all in [0, 2] for cosine.
func TestTyped_CosineMetric(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{Metric: vec.Cosine})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	matches, err := tbl.KNNSlice(ctx, fixtureQuery, len(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != len(fixture) {
		t.Fatalf("got %d matches, want %d", len(matches), len(fixture))
	}
	for i := 1; i < len(matches); i++ {
		if matches[i].Distance < matches[i-1].Distance {
			t.Errorf("distances not monotonically non-decreasing at [%d]: %v", i, matches)
		}
	}
	// Cosine distance is in [0, 2].
	for i, m := range matches {
		if m.Distance < 0 || m.Distance > 2 {
			t.Errorf("[%d] cosine distance %f out of [0, 2]", i, m.Distance)
		}
	}
}

// TestTyped_DotMetric exercises the Dot metric path (mapped to sqlite-vec's
// inner-product metric). We can't assert specific distance values because
// sqlite-vec's IP metric is implementation-specific, but we can assert:
//  1. Create + BatchInsert + KNN succeed.
//  2. The result set has every input row (k = N).
//  3. Distances are non-decreasing across the result list.
//
// That's enough to catch regressions where the "ip" keyword stops being
// honored or KNN ordering breaks.
func TestTyped_DotMetric(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{Metric: vec.Dot})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	matches, err := tbl.KNNSlice(ctx, fixtureQuery, len(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != len(fixture) {
		t.Fatalf("got %d matches, want %d", len(matches), len(fixture))
	}
	for i := 1; i < len(matches); i++ {
		if matches[i].Distance < matches[i-1].Distance {
			t.Errorf("Dot metric distances not monotonic at [%d]: %+v", i, matches)
		}
	}
}

// TestTyped_BatchInsert_OneTx asserts BatchInsert wraps every item in a
// single transaction by installing a commit hook on the underlying *Conn
// before the call and counting commits. Without the wrapping, we'd see one
// commit per item (4 commits for 4 items); with it, we expect exactly 1.
//
// Uses Conn.Raw to reach the *Conn so we can install the per-conn hook;
// pins MaxOpenConns to 1 so subsequent BatchInsert reuses the same conn
// the hook was installed on.
func TestTyped_BatchInsert_OneTx(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Install the commit hook before any vec work happens, otherwise the
	// CREATE VIRTUAL TABLE's autocommit pre-counts against us. With
	// MaxOpenConns=1 the pool will reuse this same physical conn for
	// subsequent db.ExecContext calls, so the hook stays attached.
	var commits int32
	if err := sc.Raw(func(dc any) error {
		c, ok := dc.(*sqlite.Conn)
		if !ok {
			return errors.New("driver conn is not *sqlite.Conn")
		}
		c.RegisterCommitHook(func() int32 { commits++; return 0 })
		return nil
	}); err != nil {
		sc.Close()
		t.Fatal(err)
	}
	// Release the pinned conn back to the pool so vec.Create (which calls
	// db.ExecContext) can grab it. Hook stays installed on the physical
	// conn the pool then hands back.
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}

	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// CREATE VIRTUAL TABLE counts as one autocommit; capture the baseline so
	// we can assert the BatchInsert delta exactly.
	baseline := commits

	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	got := commits - baseline
	if got != 1 {
		t.Errorf("BatchInsert fired %d commits, want 1", got)
	}
}

// TestTyped_Delete_RemovesRow inserts a vector, deletes it, and verifies
// subsequent KNN queries no longer return that rowid.
func TestTyped_Delete_RemovesRow(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Delete(ctx, 2); err != nil {
		t.Fatal(err)
	}
	matches, err := tbl.KNNSlice(ctx, fixtureQuery, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		if m.Rowid == 2 {
			t.Errorf("rowid 2 still present after Delete")
		}
	}
}

// TestTyped_Update_ChangesEmbedding asserts that Update actually overwrites
// the stored embedding (same rowid, new vector pushes the row's distance to
// the new value). The method is a thin alias for Insert; the test exists so
// a future refactor that breaks the semantic doesn't slip past silently.
func TestTyped_Update_ChangesEmbedding(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Insert at rowid 1 pointing in one direction.
	if err := tbl.Insert(ctx, 1, []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	// Update to point in the opposite direction.
	if err := tbl.Update(ctx, 1, []float32{0, 1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	// Query close to the new direction; rowid 1 should match with small distance.
	matches, err := tbl.KNNSlice(ctx, []float32{0, 0.99, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Rowid != 1 {
		t.Fatalf("matches=%+v, want [{Rowid:1, …}]", matches)
	}
	if matches[0].Distance > 0.1 {
		t.Errorf("after Update, distance=%f should be small (close to new vector)", matches[0].Distance)
	}
}

// TestTyped_Insert_DimMismatch ensures we don't ship malformed vectors to
// SQLite, since sqlite-vec's error messages there aren't always actionable.
func TestTyped_Insert_DimMismatch(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	err = tbl.Insert(ctx, 1, []float32{1, 2, 3})
	if err == nil {
		t.Fatal("expected error from dim mismatch")
	}
}

// TestTyped_KNN_StreamingIteratorBreak verifies break inside the range-over-
// func loop short-circuits the underlying *sql.Rows correctly. This is the
// happy path for iter.Seq2 consumers that only want the top match.
func TestTyped_KNN_StreamingIteratorBreak(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	var first vec.Neighbor
	count := 0
	for m, err := range tbl.KNN(ctx, fixtureQuery, 4) {
		if err != nil {
			t.Fatal(err)
		}
		first = m
		count++
		break
	}
	if count != 1 {
		t.Fatalf("expected 1 iteration before break, got %d", count)
	}
	if first.Rowid != 2 {
		t.Errorf("first match rowid=%d, want 2", first.Rowid)
	}

	// Confirm the DB is still healthy after the early break (rows.Close
	// happened via defer in the iterator).
	if _, err := tbl.KNNSlice(ctx, fixtureQuery, 1); err != nil {
		t.Fatalf("subsequent query after break: %v", err)
	}
}

// TestTyped_Open_OnExistingTable asserts Open does not issue CREATE and
// produces a working handle when the table already exists.
func TestTyped_Open_OnExistingTable(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	first, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	second, err := vec.Open(db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := second.KNNSlice(ctx, fixtureQuery, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Rowid != 2 {
		t.Errorf("Open: KNN result %+v unexpected", matches)
	}
}

// TestTyped_Drop is the final cleanup contract.
func TestTyped_Drop(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.Drop(ctx); err != nil {
		t.Fatal(err)
	}
	// After drop, KNN should fail because the table is gone.
	_, err = tbl.KNNSlice(ctx, []float32{0, 0, 0, 0}, 1)
	if err == nil {
		t.Errorf("expected error querying dropped table")
	}
}

// TestKNNSlice_NonPositiveK pins the negative-k clamp in KNNSlice. The
// streaming KNN iter already short-circuits on k<=0; this test asserts
// that KNNSlice does not panic in its make() before reaching the iter.
func TestKNNSlice_NonPositiveK(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "docs", 8, vec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	for _, k := range []int{0, -1, -1024} {
		got, err := tbl.KNNSlice(ctx, fixtureQuery, k)
		if err != nil {
			t.Errorf("k=%d: unexpected error: %v", k, err)
		}
		if len(got) != 0 {
			t.Errorf("k=%d: got %d matches, want 0", k, len(got))
		}
	}
}
