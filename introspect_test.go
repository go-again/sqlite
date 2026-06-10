package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
)

func TestTableColumnMetadata(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE TABLE t(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL COLLATE NOCASE)`); err != nil {
		t.Fatal(err)
	}

	id, err := c.TableColumnMetadata("main", "t", "id")
	if err != nil {
		t.Fatalf("metadata id: %v", err)
	}
	if !id.PrimaryKey || !id.AutoInc {
		t.Errorf("id meta = %+v, want PrimaryKey+AutoInc", id)
	}
	if id.DeclType != "INTEGER" {
		t.Errorf("id DeclType = %q, want INTEGER", id.DeclType)
	}

	// schema="" searches every attached database.
	name, err := c.TableColumnMetadata("", "t", "name")
	if err != nil {
		t.Fatalf("metadata name: %v", err)
	}
	if !name.NotNull {
		t.Errorf("name should be NOT NULL: %+v", name)
	}
	if name.CollSeq != "NOCASE" {
		t.Errorf("name CollSeq = %q, want NOCASE", name.CollSeq)
	}

	if _, err := c.TableColumnMetadata("main", "t", "nope"); err == nil {
		t.Error("metadata for an unknown column should error")
	}
}

func TestConnStatus(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sc.ExecContext(ctx, `SELECT count(*) FROM t`); err != nil {
		t.Fatal(err)
	}

	cur, hi, err := c.Status(DBStatusCacheUsed, false)
	if err != nil {
		t.Fatalf("Status(CacheUsed): %v", err)
	}
	if cur < 0 || hi < 0 {
		t.Errorf("Status(CacheUsed) = (%d, %d), want non-negative", cur, hi)
	}
	// Reset path is accepted.
	if _, _, err := c.Status(DBStatusCacheHit, true); err != nil {
		t.Fatalf("Status(CacheHit, reset): %v", err)
	}
}

func TestConnTxnState(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	if got := c.TxnState("main"); got != TxnNone {
		t.Errorf("autocommit TxnState = %v, want none", got)
	}

	tx, err := sc.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if got := c.TxnState("main"); got != TxnWrite {
		t.Errorf("in write transaction TxnState = %v, want write", got)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := c.TxnState("main"); got != TxnNone {
		t.Errorf("after rollback TxnState = %v, want none", got)
	}
}

func TestStmtReadonly(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}

	sel, err := c.Prepare(`SELECT x FROM t`)
	if err != nil {
		t.Fatal(err)
	}
	defer sel.Close()
	if !sel.(*Stmt).Readonly() {
		t.Error("SELECT should be Readonly")
	}

	ins, err := c.Prepare(`INSERT INTO t VALUES (?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer ins.Close()
	if ins.(*Stmt).Readonly() {
		t.Error("INSERT should not be Readonly")
	}
}

func TestStmtStatus(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	for i := range 64 {
		if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}

	prep, err := c.Prepare(`SELECT x FROM t WHERE x > 0`)
	if err != nil {
		t.Fatal(err)
	}
	defer prep.Close()
	st := prep.(*Stmt)

	// Run a full scan to accumulate counters.
	rs, err := st.QueryContext(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	row := make([]driver.Value, 1)
	for {
		if err := rs.Next(row); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
	}
	_ = rs.Close()

	if vm := st.Status(StmtStatusVMStep, false); vm <= 0 {
		t.Errorf("StmtStatusVMStep = %d after a full scan, want > 0", vm)
	}
	if fs := st.Status(StmtStatusFullscanStep, false); fs <= 0 {
		t.Errorf("StmtStatusFullscanStep = %d after a full table scan, want > 0", fs)
	}
}
