package memdb_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs/memdb"
)

func TestMemdb_RoundTrip(t *testing.T) {
	name, fs, err := memdb.New(memdb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	db, err := sql.Open("sqlite", "file:/x?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 25 {
		if _, err := db.ExecContext(ctx, `INSERT INTO t(v) VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 25 {
		t.Errorf("count=%d, want 25", n)
	}
}

func TestMemdb_SharedDBSeenByMultipleHandles(t *testing.T) {
	name, fs, err := memdb.New(memdb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	writer, err := sql.Open("sqlite", "file:/shared?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)

	ctx := context.Background()
	if _, err := writer.ExecContext(ctx, `CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO t VALUES ('hi')`); err != nil {
		t.Fatal(err)
	}

	reader, err := sql.Open("sqlite", "file:/shared?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var got string
	if err := reader.QueryRowContext(ctx, `SELECT v FROM t`).Scan(&got); err != nil {
		t.Fatalf("read from second handle: %v", err)
	}
	if got != "hi" {
		t.Errorf("got %q, want %q", got, "hi")
	}
}

func TestMemdb_PrivateDBIsolated(t *testing.T) {
	name, fs, err := memdb.New(memdb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	w1, _ := sql.Open("sqlite", "file:scratch?vfs="+name)
	defer w1.Close()
	w2, _ := sql.Open("sqlite", "file:scratch?vfs="+name)
	defer w2.Close()
	w1.SetMaxOpenConns(1)
	w2.SetMaxOpenConns(1)

	ctx := context.Background()
	if _, err := w1.ExecContext(ctx, `CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w1.ExecContext(ctx, `INSERT INTO t VALUES ('w1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := w2.QueryContext(ctx, `SELECT * FROM t`); err == nil {
		t.Error("private DB leaked across opens")
	}
}

func TestMemdb_WriteVisibleImmediately(t *testing.T) {
	// Distinguishing feature vs vfs/mvcc: a write done by one handle
	// is visible to another already-open handle on the same shared DB
	// instantly, with no snapshot/commit dance.
	name, fs, err := memdb.New(memdb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	a, _ := sql.Open("sqlite", "file:/race?vfs="+name)
	defer a.Close()
	b, _ := sql.Open("sqlite", "file:/race?vfs="+name)
	defer b.Close()
	a.SetMaxOpenConns(1)
	b.SetMaxOpenConns(1)

	ctx := context.Background()
	if _, err := a.ExecContext(ctx, `CREATE TABLE t(v INT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ExecContext(ctx, `INSERT INTO t VALUES (2)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("reader count=%d, want 2 (memdb has no snapshot isolation)", n)
	}
}

func TestMemdb_Close_Idempotent(t *testing.T) {
	_, fs, err := memdb.New(memdb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
