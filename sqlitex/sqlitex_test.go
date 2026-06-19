package sqlitex_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "gosqlite.org"
	"gosqlite.org/sqlitex"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1) // :memory: lives on one conn; keep it stable
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSave_CommitAndRollback(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}

	// A savepoint that commits (nil *err).
	func() (err error) {
		release, e := sqlitex.Save(ctx, conn)
		if e != nil {
			t.Fatal(e)
		}
		defer release(&err)
		_, err = conn.ExecContext(ctx, `INSERT INTO t VALUES (1)`)
		return err
	}()

	// A savepoint that rolls back (non-nil *err).
	func() (err error) {
		release, e := sqlitex.Save(ctx, conn)
		if e != nil {
			t.Fatal(e)
		}
		defer release(&err)
		if _, err = conn.ExecContext(ctx, `INSERT INTO t VALUES (2)`); err != nil {
			return err
		}
		return errors.New("force rollback")
	}()

	n, err := sqlitex.ResultInt(ctx, conn, `SELECT count(*) FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("row count = %d, want 1 (rolled-back savepoint should leave no row)", n)
	}
}

func TestTransaction(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if err := sqlitex.ExecScript(ctx, db, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}

	if err := sqlitex.Transaction(ctx, db, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `INSERT INTO t VALUES (1)`)
		return e
	}); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}

	wantErr := errors.New("rollback me")
	err := sqlitex.Transaction(ctx, db, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `INSERT INTO t VALUES (2)`); e != nil {
			return e
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("Transaction err = %v, want %v", err, wantErr)
	}

	n, _ := sqlitex.ResultInt(ctx, db, `SELECT count(*) FROM t`)
	if n != 1 {
		t.Errorf("row count = %d, want 1 (rolled-back tx should leave no row)", n)
	}
}

func TestImmediateTransaction(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}

	if err := sqlitex.ImmediateTransaction(ctx, conn, func(c *sql.Conn) error {
		_, e := c.ExecContext(ctx, `INSERT INTO t VALUES (1)`)
		return e
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	_ = sqlitex.ImmediateTransaction(ctx, conn, func(c *sql.Conn) error {
		_, _ = c.ExecContext(ctx, `INSERT INTO t VALUES (2)`)
		return errors.New("rollback")
	})

	n, _ := sqlitex.ResultInt(ctx, conn, `SELECT count(*) FROM t`)
	if n != 1 {
		t.Errorf("row count = %d, want 1", n)
	}
}

func TestResultHelpers(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	if v, err := sqlitex.ResultInt(ctx, db, `SELECT 42`); err != nil || v != 42 {
		t.Errorf("ResultInt = %d, %v", v, err)
	}
	if v, err := sqlitex.ResultText(ctx, db, `SELECT 'hello'`); err != nil || v != "hello" {
		t.Errorf("ResultText = %q, %v", v, err)
	}
	if v, err := sqlitex.ResultFloat(ctx, db, `SELECT 3.5`); err != nil || v != 3.5 {
		t.Errorf("ResultFloat = %v, %v", v, err)
	}
	if v, err := sqlitex.ResultBool(ctx, db, `SELECT 1`); err != nil || !v {
		t.Errorf("ResultBool = %v, %v", v, err)
	}
	if _, err := sqlitex.ResultInt(ctx, db, `SELECT 1 WHERE 0`); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("ResultInt no-rows err = %v, want sql.ErrNoRows", err)
	}
}
