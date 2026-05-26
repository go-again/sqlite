package sql_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/go-again/sqlite"
)

func TestVacuum_Basic(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1), (2), (3)`)
	mustExec(t, db, `delete from t where v = 2`)
	// VACUUM rebuilds the DB; should not error and the data should survive.
	mustExec(t, db, `vacuum`)

	rows := scanAll(t, db, `select v from t order by v`)
	if len(rows) != 2 {
		t.Errorf("after VACUUM: rows=%d, want 2", len(rows))
	}
}

func TestVacuum_Into(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1), (2), (3)`)

	tmp := filepath.Join(t.TempDir(), "vacuum-into.db")
	mustExec(t, db, `vacuum into ?`, tmp)
	defer os.Remove(tmp)

	// Open the new file and confirm rows were copied.
	db2, err := sql.Open("sqlite", tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	var n int
	if err := db2.QueryRow(`select count(*) from t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("VACUUM INTO: rows=%d, want 3", n)
	}
}

func TestVacuum_AutoVacuumFull(t *testing.T) {
	// AutoVacuum FULL must be set before any table creation. Use a fresh
	// file-backed DB so the setting persists across the test.
	tmp := filepath.Join(t.TempDir(), "auto-vacuum.db")
	db, err := sql.Open("sqlite", tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mustExec(t, db, `pragma auto_vacuum = FULL`)
	mustExec(t, db, `create table t (v int)`)

	// auto_vacuum is read-only after the first table is created, so the
	// setting we read back should be FULL (1).
	var mode int
	if err := db.QueryRow(`pragma auto_vacuum`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 1 {
		t.Errorf("auto_vacuum=%d, want 1 (FULL)", mode)
	}
}

func TestVacuum_IncrementalVacuum(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "incr-vacuum.db")
	db, err := sql.Open("sqlite", tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mustExec(t, db, `pragma auto_vacuum = INCREMENTAL`)
	mustExec(t, db, `create table t (v blob)`)
	mustExec(t, db, `insert into t values (randomblob(4096))`)
	mustExec(t, db, `delete from t`)
	// incremental_vacuum reclaims freed pages.
	mustExec(t, db, `pragma incremental_vacuum`)
}

func TestVacuum_FreelistCount(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v blob)`)
	mustExec(t, db, `insert into t values (randomblob(4096))`)
	mustExec(t, db, `delete from t`)

	var n int
	scanOne(t, db, &n, `pragma freelist_count`)
	if n < 0 {
		t.Errorf("freelist_count=%d, want >= 0", n)
	}
}
