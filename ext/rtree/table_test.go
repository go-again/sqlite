package rtree_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/ext/rtree"
	"gosqlite.org/internal/testhelp"
)

// openTableDB returns an in-memory *sql.DB. The rtree vtab is built into the
// library, so create/insert/Search need no registration; pass withCircle=true
// to also register the circle geometry (pool-wide, pinned) for SearchCircle.
func openTableDB(t *testing.T, withCircle bool) *sql.DB {
	t.Helper()
	if withCircle {
		testhelp.WithConnectHook(t, rtree.Register)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedPOIs inserts five points of interest on the plane.
func seedPOIs(t *testing.T, ctx context.Context, tbl *rtree.Table) {
	t.Helper()
	for _, p := range []struct {
		id   int64
		x, y float64
	}{
		{1, 1, 1}, {2, 2, 5}, {3, 5, 5}, {4, 8, 1}, {5, 5, 9},
	} {
		if err := tbl.InsertPoint(ctx, p.id, p.x, p.y); err != nil {
			t.Fatalf("InsertPoint %d: %v", p.id, err)
		}
	}
}

func TestTable_CreateInsertSearch(t *testing.T) {
	ctx := context.Background()
	db := openTableDB(t, false) // bounding-box search needs no registration
	tbl, err := rtree.Create(ctx, db, "pois")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tbl.Dimensions() != 2 {
		t.Fatalf("Dimensions = %d, want 2", tbl.Dimensions())
	}
	seedPOIs(t, ctx, tbl)

	// Boxes overlapping the rectangle x∈[0,3], y∈[0,6]: ids 1 (1,1) and 2 (2,5).
	ids, err := tbl.Search(ctx, 0, 3, 0, 6)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := []int64{1, 2}; !equalIDs(ids, want) {
		t.Errorf("Search box = %v, want %v", ids, want)
	}
}

func TestTable_SearchCircle(t *testing.T) {
	ctx := context.Background()
	db := openTableDB(t, true) // circle geometry registered pool-wide
	tbl, err := rtree.Create(ctx, db, "pois")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedPOIs(t, ctx, tbl)

	// Within 1.5 of (5,5): only id 3 sits exactly there.
	ids, err := tbl.SearchCircle(ctx, 5, 5, 1.5)
	if err != nil {
		t.Fatalf("SearchCircle: %v", err)
	}
	if want := []int64{3}; !equalIDs(ids, want) {
		t.Errorf("SearchCircle(5,5,1.5) = %v, want %v", ids, want)
	}

	// Within 4.5 of (5,5): ids 2 (2,5)→3, 3 (5,5)→0, 5 (5,9)→4 all qualify.
	ids, err = tbl.SearchCircle(ctx, 5, 5, 4.5)
	if err != nil {
		t.Fatalf("SearchCircle wide: %v", err)
	}
	if want := []int64{2, 3, 5}; !equalIDs(ids, want) {
		t.Errorf("SearchCircle(5,5,4.5) = %v, want %v", ids, want)
	}
}

func TestTable_Delete(t *testing.T) {
	ctx := context.Background()
	db := openTableDB(t, false)
	tbl, err := rtree.Create(ctx, db, "pois")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedPOIs(t, ctx, tbl)
	if err := tbl.Delete(ctx, 2); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	ids, err := tbl.Search(ctx, 0, 3, 0, 6)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := []int64{1}; !equalIDs(ids, want) {
		t.Errorf("after delete id 2, Search = %v, want %v", ids, want)
	}
}

func TestTable_Open_DetectsDimensions(t *testing.T) {
	ctx := context.Background()
	db := openTableDB(t, false)
	if _, err := rtree.Create(ctx, db, "cube", rtree.WithDimensions(3)); err != nil {
		t.Fatalf("Create 3D: %v", err)
	}
	tbl, err := rtree.Open(ctx, db, "cube")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if tbl.Dimensions() != 3 {
		t.Errorf("Open detected %d dimensions, want 3", tbl.Dimensions())
	}
	// A 3D box insert + search round-trips.
	if err := tbl.Insert(ctx, 1, 0, 1, 0, 1, 0, 1); err != nil {
		t.Fatalf("Insert 3D: %v", err)
	}
	ids, err := tbl.Search(ctx, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5)
	if err != nil {
		t.Fatalf("Search 3D: %v", err)
	}
	if want := []int64{1}; !equalIDs(ids, want) {
		t.Errorf("3D Search = %v, want %v", ids, want)
	}
}

func TestTable_InsertWrongArity(t *testing.T) {
	ctx := context.Background()
	db := openTableDB(t, false)
	tbl, err := rtree.Create(ctx, db, "pois")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := tbl.Insert(ctx, 1, 0, 1, 0); err == nil {
		t.Error("Insert with 3 coords into a 2D table should error")
	}
	if err := tbl.InsertPoint(ctx, 1, 0, 1, 2); err == nil {
		t.Error("InsertPoint with 3 coords into a 2D table should error")
	}
}

func TestTable_IfNotExists_And_ErrAlreadyExists(t *testing.T) {
	ctx := context.Background()
	db := openTableDB(t, false)
	if _, err := rtree.Create(ctx, db, "t"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := rtree.Create(ctx, db, "t"); !errors.Is(err, rtree.ErrAlreadyExists) {
		t.Errorf("second Create error = %v, want ErrAlreadyExists", err)
	}
	if _, err := rtree.Create(ctx, db, "t", rtree.WithIfNotExists()); err != nil {
		t.Errorf("Create WithIfNotExists: %v", err)
	}
}

func TestTable_Drop(t *testing.T) {
	ctx := context.Background()
	db := openTableDB(t, false)
	tbl, err := rtree.Create(ctx, db, "t")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := tbl.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := rtree.Create(ctx, db, "t"); err != nil {
		t.Errorf("Create after Drop: %v", err)
	}
}

// Guard the typed-handle constructor signatures against drift.
var (
	_ func(context.Context, *sql.DB, string, ...rtree.CreateOption) (*rtree.Table, error) = rtree.Create
	_ func(*sqlite.Conn) error                                                            = rtree.Register
)
