package pivot_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/go-again/sqlite/ext/pivot"
	"github.com/go-again/sqlite/internal/testhelp"
)

func openDB(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	testhelp.WithConnectHook(t, pivot.Register)
	return testhelp.OpenPinned(t, "sqlite", ":memory:")
}

func seed(t *testing.T, sc *sql.Conn) {
	t.Helper()
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE sales(region TEXT, product TEXT, units INTEGER)`); err != nil {
		t.Fatal(err)
	}
	rows := [][]any{
		{"north", "apple", 10},
		{"north", "banana", 5},
		{"south", "apple", 20},
		{"south", "banana", 7},
		{"south", "cherry", 3},
	}
	for _, r := range rows {
		if _, err := sc.ExecContext(ctx, `INSERT INTO sales VALUES (?, ?, ?)`, r...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPivot_CrossTab(t *testing.T) {
	_, sc := openDB(t)
	seed(t, sc)
	ctx := context.Background()
	_, err := sc.ExecContext(ctx, `
		CREATE VIRTUAL TABLE p USING pivot(
		    'SELECT DISTINCT region FROM sales ORDER BY region',
		    'SELECT product, product FROM (SELECT DISTINCT product FROM sales ORDER BY product)',
		    'SELECT SUM(units) FROM sales WHERE region = ? AND product = ?'
		)`)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := sc.QueryContext(ctx, `SELECT * FROM p ORDER BY region`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 4 {
		t.Errorf("cols=%v, want 4 (region + 3 products)", cols)
	}
	type row struct {
		region                string
		apple, banana, cherry sql.NullInt64
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.region, &r.apple, &r.banana, &r.cherry); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("rows=%v, want 2", got)
	}
	// Expectation: north=10/5/nil, south=20/7/3.
	north, south := got[0], got[1]
	if north.region != "north" || north.apple.Int64 != 10 || north.banana.Int64 != 5 {
		t.Errorf("north row wrong: %+v", north)
	}
	if north.cherry.Valid {
		t.Errorf("north cherry should be NULL, got %v", north.cherry.Int64)
	}
	if south.region != "south" || south.apple.Int64 != 20 || south.banana.Int64 != 7 || south.cherry.Int64 != 3 {
		t.Errorf("south row wrong: %+v", south)
	}
}

func TestPivot_BadArgs(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE VIRTUAL TABLE bad USING pivot('SELECT 1')`); err == nil {
		t.Error("missing args: want error, got nil")
	}
}

func TestPivot_CellBindCountMismatch(t *testing.T) {
	_, sc := openDB(t)
	seed(t, sc)
	_, err := sc.ExecContext(context.Background(), `
		CREATE VIRTUAL TABLE p USING pivot(
		    'SELECT DISTINCT region FROM sales',
		    'SELECT product, product FROM sales',
		    'SELECT 0'  -- 0 binds, expects 2
		)`)
	if err == nil {
		t.Error("bind-count mismatch: want error, got nil")
	}
}
