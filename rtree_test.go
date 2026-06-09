package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

// boxesOverlap reports whether two axis-aligned boxes, each laid out as
// [minX, maxX, minY, maxY], intersect.
func boxesOverlap(box, query []float64) bool {
	return box[0] <= query[1] && box[1] >= query[0] &&
		box[2] <= query[3] && box[3] >= query[2]
}

// setupRTree builds a 2D R-Tree on the pinned conn and seeds four boxes:
// id 1 and id 3 overlap the unit square [0,1]×[0,1]; id 2 and id 4 are far
// away. It returns the pinned *sql.Conn (for queries on the same conn the
// geometry is registered on) and the underlying *Conn (for registration).
func setupRTree(t *testing.T) (context.Context, *sql.Conn, *Conn) {
	t.Helper()
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE demo USING rtree(id, minX, maxX, minY, maxY)`); err != nil {
		t.Fatalf("create rtree: %v", err)
	}
	for _, b := range [][5]float64{
		{1, 0, 1, 0, 1},
		{2, 10, 11, 10, 11},
		{3, 0.5, 1.5, 0.5, 1.5},
		{4, 5, 6, 5, 6},
	} {
		if _, err := sc.ExecContext(ctx,
			`INSERT INTO demo(id, minX, maxX, minY, maxY) VALUES (?, ?, ?, ?, ?)`,
			int64(b[0]), b[1], b[2], b[3], b[4]); err != nil {
			t.Fatalf("insert box %v: %v", b, err)
		}
	}
	return ctx, sc, c
}

func queryRTreeIDs(t *testing.T, ctx context.Context, sc *sql.Conn, query string) []int64 {
	t.Helper()
	rows, err := sc.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("query: %v", err)
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

func TestRTreeGeometry(t *testing.T) {
	ctx, sc, c := setupRTree(t)

	overlaps := func(coords, params []float64) (bool, error) {
		if len(coords) < 4 || len(params) < 4 {
			return false, fmt.Errorf("overlaps: want 4 coords/params, got %d/%d", len(coords), len(params))
		}
		return boxesOverlap(coords, params), nil
	}
	if err := c.RegisterRTreeGeometry("overlaps", overlaps); err != nil {
		t.Fatalf("RegisterRTreeGeometry: %v", err)
	}

	ids := queryRTreeIDs(t, ctx, sc,
		`SELECT id FROM demo WHERE id MATCH overlaps(0, 1, 0, 1) ORDER BY id`)
	if want := []int64{1, 3}; !int64sEqual(ids, want) {
		t.Errorf("geometry MATCH ids = %v, want %v", ids, want)
	}
}

func TestRTreeQuery(t *testing.T) {
	ctx, sc, c := setupRTree(t)

	qoverlaps := func(info *RTreeQueryInfo) (RTreeWithin, float64, error) {
		if len(info.Coords) < 4 || len(info.Params) < 4 {
			return RTreeNotWithin, 0, fmt.Errorf("qoverlaps: want 4 coords/params")
		}
		if !boxesOverlap(info.Coords, info.Params) {
			return RTreeNotWithin, 0, nil
		}
		// Score by the box's minX so nearer boxes are visited first;
		// PartlyWithin keeps the row as a candidate.
		return RTreePartlyWithin, info.Coords[0], nil
	}
	if err := c.RegisterRTreeQuery("qoverlaps", qoverlaps); err != nil {
		t.Fatalf("RegisterRTreeQuery: %v", err)
	}

	ids := queryRTreeIDs(t, ctx, sc,
		`SELECT id FROM demo WHERE id MATCH qoverlaps(0, 1, 0, 1) ORDER BY id`)
	if want := []int64{1, 3}; !int64sEqual(ids, want) {
		t.Errorf("query-callback MATCH ids = %v, want %v", ids, want)
	}
}

func int64sEqual(a, b []int64) bool {
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
