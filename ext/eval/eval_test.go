package eval_test

import (
	"context"
	"database/sql"
	"testing"

	"gosqlite.org/ext/eval"
	"gosqlite.org/internal/testhelp"
)

func openDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	testhelp.WithConnectHook(t, eval.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return context.Background(), db
}

func TestEval_Scalar(t *testing.T) {
	ctx, db := openDB(t)
	var got string
	if err := db.QueryRowContext(ctx, `SELECT eval('SELECT 6 * 7')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "42" {
		t.Errorf("eval('SELECT 6*7') = %q, want 42", got)
	}
}

func TestEval_MultiRowWithSeparator(t *testing.T) {
	ctx, db := openDB(t)
	if _, err := db.ExecContext(ctx, `CREATE TABLE t(name TEXT); INSERT INTO t VALUES ('alice'),('bob'),('carol')`); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRowContext(ctx,
		`SELECT eval('SELECT name FROM t ORDER BY name', ', ')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "alice, bob, carol" {
		t.Errorf("eval = %q, want 'alice, bob, carol'", got)
	}
}

func TestEval_SideEffect(t *testing.T) {
	ctx, db := openDB(t)
	// A statement with no result columns applies its side effect and
	// returns NULL.
	var v any
	if err := db.QueryRowContext(ctx, `SELECT eval('CREATE TABLE made(x)')`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("eval(DDL) = %v, want NULL", v)
	}
	// The table really exists now.
	if _, err := db.ExecContext(ctx, `INSERT INTO made VALUES (1)`); err != nil {
		t.Fatalf("table from eval() not usable: %v", err)
	}
}

func TestEval_Error(t *testing.T) {
	ctx, db := openDB(t)
	var s string
	if err := db.QueryRowContext(ctx, `SELECT eval('SELECT * FROM nope')`).Scan(&s); err == nil {
		t.Error("eval of a bad query did not error")
	}
}
