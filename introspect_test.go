package sqlite

import (
	"context"
	"database/sql"
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

func TestTableColumnMetadata_SpecialModes(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}

	// column="" probes table existence / rowid presence — succeeds for a real
	// table, errors for a missing one.
	if _, err := c.TableColumnMetadata("main", "t", ""); err != nil {
		t.Errorf("rowid-probe of existing table errored: %v", err)
	}
	if _, err := c.TableColumnMetadata("main", "missing", ""); err == nil {
		t.Error("rowid-probe of a missing table should error")
	}

	// WITHOUT ROWID table — the TEXT PRIMARY KEY column reports PrimaryKey.
	if _, err := sc.ExecContext(ctx,
		`CREATE TABLE wr(k TEXT PRIMARY KEY, v TEXT) WITHOUT ROWID`); err != nil {
		t.Fatal(err)
	}
	md, err := c.TableColumnMetadata("main", "wr", "k")
	if err != nil {
		t.Fatalf("WITHOUT ROWID metadata: %v", err)
	}
	if !md.PrimaryKey {
		t.Errorf("wr.k should be PrimaryKey: %+v", md)
	}
	if md.DeclType != "TEXT" {
		t.Errorf("wr.k DeclType = %q, want TEXT", md.DeclType)
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

func TestConnStatus_Reset(t *testing.T) {
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
	if _, err := sc.ExecContext(ctx, `SELECT count(*) FROM t`); err != nil {
		t.Fatal(err)
	}

	// LOOKASIDE_USED maintains a high-water mark (unlike CACHE_USED). Reset
	// collapses it to the current value, so a subsequent read must report
	// high-water == current.
	if _, _, err := c.Status(DBStatusLookasideUsed, true); err != nil {
		t.Fatalf("Status reset: %v", err)
	}
	cur, hi, err := c.Status(DBStatusLookasideUsed, false)
	if err != nil {
		t.Fatal(err)
	}
	if hi != cur {
		t.Errorf("after reset, high-water = %d, current = %d; want equal", hi, cur)
	}
}

func TestConnTxnState_Read(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x); INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	tx, err := sc.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.QueryContext(ctx, `SELECT count(*) FROM t`); err != nil { // start the read
		t.Fatal(err)
	}
	if got := c.TxnState("main"); got != TxnRead {
		t.Errorf("read-only transaction TxnState = %v, want read", got)
	}
	if got := TxnRead.String(); got != "read" {
		t.Errorf("TxnRead.String() = %q, want read", got)
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
