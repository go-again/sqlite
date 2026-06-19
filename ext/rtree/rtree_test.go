package rtree_test

import (
	"context"
	"database/sql"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/ext/rtree"
	"gosqlite.org/internal/testhelp"
)

// openRTreeDB returns a *sql.DB with the circle geometry registered on every
// connection, pinned to one conn, seeded with three 2D boxes.
func openRTreeDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	testhelp.WithConnectHook(t, rtree.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`CREATE VIRTUAL TABLE demo USING rtree(id, minX, maxX, minY, maxY)`); err != nil {
		t.Fatalf("create rtree: %v", err)
	}
	for _, b := range [][5]float64{
		{1, 0, 1, 0, 1},
		{2, 10, 11, 10, 11},
		{3, 5, 6, 5, 6},
	} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO demo(id, minX, maxX, minY, maxY) VALUES (?, ?, ?, ?, ?)`,
			int64(b[0]), b[1], b[2], b[3], b[4]); err != nil {
			t.Fatalf("insert box %v: %v", b, err)
		}
	}
	return ctx, db
}

func matchCircle(t *testing.T, ctx context.Context, db *sql.DB, cx, cy, r float64) []int64 {
	t.Helper()
	rows, err := db.QueryContext(ctx,
		`SELECT id FROM demo WHERE id MATCH circle(?, ?, ?) ORDER BY id`, cx, cy, r)
	if err != nil {
		t.Fatalf("circle query: %v", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCircle_NearOrigin(t *testing.T) {
	ctx, db := openRTreeDB(t)
	if ids := matchCircle(t, ctx, db, 0, 0, 1.5); !equalIDs(ids, []int64{1}) {
		t.Errorf("circle(0,0,1.5) = %v, want [1]", ids)
	}
}

func TestCircle_InsideFarBox(t *testing.T) {
	ctx, db := openRTreeDB(t)
	if ids := matchCircle(t, ctx, db, 10.5, 10.5, 1); !equalIDs(ids, []int64{2}) {
		t.Errorf("circle(10.5,10.5,1) = %v, want [2]", ids)
	}
}

func TestCircle_Empty(t *testing.T) {
	ctx, db := openRTreeDB(t)
	if ids := matchCircle(t, ctx, db, 100, 100, 1); len(ids) != 0 {
		t.Errorf("circle(100,100,1) = %v, want none", ids)
	}
}

func TestCircle_ArgCountError(t *testing.T) {
	ctx, db := openRTreeDB(t)
	// circle wants exactly 3 args; 2 must surface an error.
	if _, err := db.QueryContext(ctx,
		`SELECT id FROM demo WHERE id MATCH circle(0, 0)`); err == nil {
		t.Error("circle with 2 args should error")
	}
}

// Guard against accidental signature drift on the exported entry point.
var _ func(*sqlite.Conn) error = rtree.Register
