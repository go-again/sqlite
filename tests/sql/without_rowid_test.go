package sql_test

import (
	"strings"
	"testing"
)

func TestWithoutRowid_Basic(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (
		id text primary key,
		v int
	) without rowid`)
	mustExec(t, db, `insert into t values ('a', 1), ('b', 2), ('c', 3)`)

	rows := scanAll(t, db, `select id, v from t order by id`)
	if len(rows) != 3 || rows[0][0].(string) != "a" {
		t.Errorf("WITHOUT ROWID: %+v", rows)
	}
}

// TestWithoutRowid_NoRowidColumn asserts that SELECT rowid fails on a
// WITHOUT ROWID table.
func TestWithoutRowid_NoRowidColumn(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id text primary key, v int) without rowid`)
	mustExec(t, db, `insert into t values ('a', 1)`)
	_, err := db.Exec(`select rowid from t`)
	if err == nil {
		t.Fatal("expected error: rowid not accessible on WITHOUT ROWID table")
	}
	if !strings.Contains(err.Error(), "rowid") && !strings.Contains(err.Error(), "no such column") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWithoutRowid_UpdateAndDelete(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id text primary key, v int) without rowid`)
	mustExec(t, db, `insert into t values ('a', 1), ('b', 2)`)
	mustExec(t, db, `update t set v = 99 where id = 'a'`)
	mustExec(t, db, `delete from t where id = 'b'`)

	var n int
	scanOne(t, db, &n, `select count(*) from t`)
	if n != 1 {
		t.Errorf("after DELETE: count=%d, want 1", n)
	}
	var v int64
	scanOne(t, db, &v, `select v from t`)
	if v != 99 {
		t.Errorf("after UPDATE: v=%d, want 99", v)
	}
}
