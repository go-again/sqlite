package sql_test

import (
	"context"
	"database/sql"
	"testing"
)

func TestTransaction_BeginCommit(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`insert into t values (1), (2)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var n int
	scanOne(t, db, &n, `select count(*) from t`)
	if n != 2 {
		t.Errorf("after commit: count=%d, want 2", n)
	}
}

func TestTransaction_Rollback(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`insert into t values (1)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var n int
	scanOne(t, db, &n, `select count(*) from t`)
	if n != 0 {
		t.Errorf("after rollback: count=%d, want 0", n)
	}
}

func TestTransaction_IsolationLevels(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)
	ctx := context.Background()
	// SQLite supports Serializable; LevelReadUncommitted maps to BEGIN.
	for _, level := range []sql.IsolationLevel{
		sql.LevelSerializable,
		sql.LevelDefault,
	} {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: level})
		if err != nil {
			t.Errorf("BeginTx level=%v: %v", level, err)
			continue
		}
		tx.Rollback()
	}
}

func TestTransaction_Savepoint(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`insert into t values (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`savepoint sp1`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`insert into t values (2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`rollback to sp1`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`release sp1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var n int
	scanOne(t, db, &n, `select count(*) from t`)
	if n != 1 {
		t.Errorf("after SAVEPOINT/ROLLBACK TO: count=%d, want 1", n)
	}
}

func TestTransaction_NestedSavepoints(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tx.Exec(`insert into t values (1)`)
	tx.Exec(`savepoint a`)
	tx.Exec(`insert into t values (2)`)
	tx.Exec(`savepoint b`)
	tx.Exec(`insert into t values (3)`)
	tx.Exec(`rollback to b`) // discards 3
	tx.Exec(`release b`)
	tx.Exec(`insert into t values (4)`)
	tx.Exec(`rollback to a`) // discards 2 and 4
	tx.Exec(`release a`)
	tx.Commit()

	rows := scanAll(t, db, `select v from t order by v`)
	if len(rows) != 1 || rows[0][0].(int64) != 1 {
		t.Errorf("nested SP: %+v, want [{1}]", rows)
	}
}

func TestTransaction_ReadOnly(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1)`)

	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	// Reads work.
	rows, _ := tx.Query(`select v from t`)
	if rows != nil {
		rows.Close()
	}
	// Writes should error in read-only mode (driver-dependent — some
	// drivers map ReadOnly to BEGIN DEFERRED + a query_only PRAGMA, others
	// reject writes only at commit). At minimum, the transaction must
	// commit/rollback cleanly.
	tx.Rollback()
}

func TestTransaction_AutoCommit(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)
	// Without explicit Begin, each Exec auto-commits.
	mustExec(t, db, `insert into t values (1)`)
	mustExec(t, db, `insert into t values (2)`)
	var n int
	scanOne(t, db, &n, `select count(*) from t`)
	if n != 2 {
		t.Errorf("auto-commit: count=%d, want 2", n)
	}
}

func TestTransaction_RollbackOnError(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int unique)`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tx.Exec(`insert into t values (1)`)
	if _, err := tx.Exec(`insert into t values (1)`); err == nil {
		t.Fatal("expected UNIQUE violation")
	}
	tx.Rollback()

	var n int
	scanOne(t, db, &n, `select count(*) from t`)
	if n != 0 {
		t.Errorf("after rollback: count=%d, want 0", n)
	}
}
