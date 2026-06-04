package sql_test

import (
	"context"
	"database/sql"
	"testing"

	sqlite "github.com/go-again/sqlite"
)

// openDB returns an in-memory database pinned to a single connection. Tests
// in this package own their own DB so they can run in parallel and don't
// share schema state.
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(sqlite.DriverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	return db
}

// mustExec runs Exec or fails the test. Args after query are bound.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) sql.Result {
	t.Helper()
	res, err := db.ExecContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
	return res
}

// scanOne runs query, scans the single resulting value into a destination
// pointer, and fails on zero or multiple rows. Use for SELECT statements
// expected to return exactly one row with one column.
func scanOne(t *testing.T, db *sql.DB, dest any, query string, args ...any) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		t.Fatalf("query %q: no rows", query)
	}
	if err := rows.Scan(dest); err != nil {
		t.Fatalf("scan %q: %v", query, err)
	}
	if rows.Next() {
		t.Fatalf("query %q: more than one row", query)
	}
}

// scanAll runs query and returns every row as []any, one slice per row.
// Useful for assertions over small result sets without declaring a struct.
func scanAll(t *testing.T, db *sql.DB, query string, args ...any) [][]any {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns %q: %v", query, err)
	}
	var out [][]any
	for rows.Next() {
		row := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range row {
			ptrs[i] = &row[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %q: %v", query, err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return out
}

// sqliteVersion returns the running SQLite version as a "X.Y.Z" string.
// Used to gate tests for features introduced in newer SQLite releases.
func sqliteVersion(t *testing.T, db *sql.DB) string {
	t.Helper()
	var v string
	scanOne(t, db, &v, `select sqlite_version()`)
	return v
}
