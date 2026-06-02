package statement_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/statement"
)

func openDB(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	d := sqlite.DefaultDriver()
	prev := d.ConnectHook
	d.ConnectHook = func(c *sqlite.Conn) error {
		if prev != nil {
			if err := prev(c); err != nil {
				return err
			}
		}
		return statement.Register(c)
	}
	t.Cleanup(func() { d.ConnectHook = prev })

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	return db, sc
}

func seed(t *testing.T, sc *sql.Conn) {
	t.Helper()
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]any{
		{"alice", 30},
		{"bob", 17},
		{"carol", 45},
		{"dave", 12},
	} {
		if _, err := sc.ExecContext(ctx, `INSERT INTO users(name, age) VALUES (?, ?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStatement_AnonymousBind(t *testing.T) {
	_, sc := openDB(t)
	seed(t, sc)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE adults USING statement('SELECT name FROM users WHERE age >= ?')`,
	); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(ctx, `SELECT name FROM adults WHERE "?1" = 18 ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"alice", "carol"}
	if len(names) != len(want) {
		t.Fatalf("rows=%v, want %v", names, want)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("row[%d]=%q, want %q", i, names[i], w)
		}
	}
}

func TestStatement_NamedBind(t *testing.T) {
	_, sc := openDB(t)
	seed(t, sc)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE matches USING statement('SELECT name FROM users WHERE name LIKE :pat')`,
	); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(ctx, `SELECT name FROM matches WHERE pat = 'a%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	if len(got) != 1 || got[0] != "alice" {
		t.Errorf("got %v, want [alice]", got)
	}
}

func TestStatement_BadArgs(t *testing.T) {
	_, sc := openDB(t)
	for _, bad := range []string{
		``,
		`CREATE VIRTUAL TABLE oops USING statement()`,
		`CREATE VIRTUAL TABLE oops USING statement('SELECT 1', 'SELECT 2')`,
	} {
		if bad == "" {
			continue
		}
		if _, err := sc.ExecContext(context.Background(), bad); err == nil {
			t.Errorf("%q: expected error, got nil", bad)
		}
	}
}

func TestStatement_NoBinds(t *testing.T) {
	_, sc := openDB(t)
	seed(t, sc)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE everyone USING statement('SELECT name FROM users ORDER BY name')`,
	); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(ctx, `SELECT name FROM everyone`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 4 {
		t.Errorf("count=%d, want 4", count)
	}
}

func TestStatement_AnonymousBindOutOfOrder(t *testing.T) {
	// Regression for the BestIndex permutation bug: with anonymous `?`
	// HIDDEN columns, constraining them in non-declaration order
	// (e.g. WHERE "?2" = X AND "?1" = Y) must still bind each arg to
	// its correct stmt position.
	_, sc := openDB(t)
	seed(t, sc)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE r USING statement('SELECT name FROM users WHERE age BETWEEN ? AND ?')`,
	); err != nil {
		t.Fatal(err)
	}
	// "?2"=45 and "?1"=18 — out of declaration order. Should bind ?1=18
	// and ?2=45, yielding adults but not centenarians.
	rows, err := sc.QueryContext(ctx,
		`SELECT name FROM r WHERE "?2" = 45 AND "?1" = 18 ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	// Correct binding: ?1=18, ?2=45 → BETWEEN 18 AND 45 → alice, carol.
	// Broken binding (WHERE-clause order): ?1=45, ?2=18 → BETWEEN 45 AND
	// 18 → empty.
	want := []string{"alice", "carol"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v (broken permutation would have returned [])", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row[%d]=%q, want %q", i, got[i], w)
		}
	}
}

// Ensure the suite can run when only this package's symbols are needed.
var _ = errors.New
