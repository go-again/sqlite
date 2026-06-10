package series_test

import (
	"context"
	"database/sql"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/series"
	"github.com/go-again/sqlite/internal/testhelp"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	testhelp.WithConnectHook(t, series.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func collect(t *testing.T, db *sql.DB, query string, args ...any) []int64 {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, v)
	}
	return out
}

func eq(a, b []int64) bool {
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

func TestSeries_Basic(t *testing.T) {
	db := openDB(t)
	got := collect(t, db, `SELECT value FROM generate_series(1, 5)`)
	if want := []int64{1, 2, 3, 4, 5}; !eq(got, want) {
		t.Errorf("generate_series(1,5) = %v, want %v", got, want)
	}
}

func TestSeries_Step(t *testing.T) {
	db := openDB(t)
	got := collect(t, db, `SELECT value FROM generate_series(0, 10, 5)`)
	if want := []int64{0, 5, 10}; !eq(got, want) {
		t.Errorf("generate_series(0,10,5) = %v, want %v", got, want)
	}
}

func TestSeries_Descending(t *testing.T) {
	db := openDB(t)
	got := collect(t, db, `SELECT value FROM generate_series(5, 1, -1)`)
	if want := []int64{5, 4, 3, 2, 1}; !eq(got, want) {
		t.Errorf("generate_series(5,1,-1) = %v, want %v", got, want)
	}
}

func TestSeries_Empty(t *testing.T) {
	db := openDB(t)
	// start > stop with positive step yields nothing.
	if got := collect(t, db, `SELECT value FROM generate_series(5, 1)`); len(got) != 0 {
		t.Errorf("generate_series(5,1) = %v, want empty", got)
	}
}

func TestSeries_Composable(t *testing.T) {
	db := openDB(t)
	// Sum of 1..100 via the series, exercising it as a real table source.
	var sum int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT sum(value) FROM generate_series(1, 100)`).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum != 5050 {
		t.Errorf("sum(generate_series(1,100)) = %d, want 5050", sum)
	}
}

func TestSeries_StepZeroErrors(t *testing.T) {
	db := openDB(t)
	if _, err := db.QueryContext(context.Background(),
		`SELECT value FROM generate_series(1, 10, 0)`); err == nil {
		t.Error("generate_series with step 0 should error")
	}
}

var _ func(*sqlite.Conn) error = series.Register
