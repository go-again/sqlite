package sql_test

import "testing"

func TestGenerated_Virtual(t *testing.T) {
	db := openDB(t)
	// Virtual generated column (default; not persisted).
	mustExec(t, db, `create table t (
		a int,
		b int,
		sum int generated always as (a + b) virtual
	)`)
	mustExec(t, db, `insert into t (a, b) values (1, 2)`)
	var s int64
	scanOne(t, db, &s, `select sum from t`)
	if s != 3 {
		t.Errorf("virtual sum=%d, want 3", s)
	}
}

func TestGenerated_Stored(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (
		a int,
		b int,
		sum int generated always as (a + b) stored
	)`)
	mustExec(t, db, `insert into t (a, b) values (5, 7)`)
	var s int64
	scanOne(t, db, &s, `select sum from t`)
	if s != 12 {
		t.Errorf("stored sum=%d, want 12", s)
	}
}

func TestGenerated_DependentColumn(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (
		fname text, lname text,
		full text generated always as (fname || ' ' || lname) stored
	)`)
	mustExec(t, db, `insert into t (fname, lname) values ('Ada', 'Lovelace')`)
	var full string
	scanOne(t, db, &full, `select full from t`)
	if full != "Ada Lovelace" {
		t.Errorf("dependent generated=%q, want 'Ada Lovelace'", full)
	}
}

func TestGenerated_AfterUpdate(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (
		v int,
		v2 int generated always as (v * 2) virtual
	)`)
	mustExec(t, db, `insert into t (v) values (5)`)
	var double int64
	scanOne(t, db, &double, `select v2 from t`)
	if double != 10 {
		t.Errorf("initial v2=%d, want 10", double)
	}
	mustExec(t, db, `update t set v = 11`)
	scanOne(t, db, &double, `select v2 from t`)
	if double != 22 {
		t.Errorf("after update v2=%d, want 22", double)
	}
}
