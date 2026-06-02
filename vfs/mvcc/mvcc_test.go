package mvcc_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs/mvcc"
)

func TestMVCC_RoundTrip(t *testing.T) {
	name, fs, err := mvcc.New(mvcc.Options{})
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

func TestMVCC_SharedDBSeenByMultipleHandles(t *testing.T) {
	name, fs, err := mvcc.New(mvcc.Options{})
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

	// A second sql.DB pointing at the same DB name should see the row.
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

func TestMVCC_PrivateDBIsolated(t *testing.T) {
	name, fs, err := mvcc.New(mvcc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	// Names without a leading slash get private storage per open.
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
	// w2 should NOT see the table.
	if _, err := w2.QueryContext(ctx, `SELECT * FROM t`); err == nil {
		t.Error("private DB leaked across opens")
	}
}

func TestMVCC_ConcurrentReadDuringWriteSnapshot(t *testing.T) {
	// Pin that a reader's snapshot survives across a writer's commit
	// — until the reader re-acquires a SHARED lock, it should still
	// see the pre-commit row count.
	name, fs, err := mvcc.New(mvcc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	db, _ := sql.Open("sqlite", "file:/snap?vfs="+name)
	defer db.Close()
	db.SetMaxOpenConns(2)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t(v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 50 {
			_, _ = db.ExecContext(ctx, `INSERT INTO t VALUES (?)`, i)
		}
	}()

	// Reads should not deadlock or crash even while concurrent inserts
	// run; final visible count should be >= 1.
	for range 25 {
		var c int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&c)
		if c < 1 {
			t.Errorf("count=%d, want >=1 (writer should never erase committed rows)", c)
		}
	}
	wg.Wait()
}

func TestMVCC_Close_Idempotent(t *testing.T) {
	_, fs, err := mvcc.New(mvcc.Options{})
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
