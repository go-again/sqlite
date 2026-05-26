package sql_test

import (
	"strings"
	"testing"
)

func TestIndex_BasicCreate(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, name text)`)
	mustExec(t, db, `create index ix_name on t(name)`)
	// Verify index exists in sqlite_master.
	var n int
	scanOne(t, db, &n, `select count(*) from sqlite_master where type='index' and name='ix_name'`)
	if n != 1 {
		t.Errorf("expected 1 ix_name index in sqlite_master, got %d", n)
	}
}

func TestIndex_Unique(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (email text)`)
	mustExec(t, db, `create unique index ix_email on t(email)`)
	mustExec(t, db, `insert into t values ('a@x')`)
	_, err := db.Exec(`insert into t values ('a@x')`)
	if err == nil {
		t.Fatal("expected UNIQUE index violation")
	}
}

func TestIndex_MultiColumn(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (a int, b int, c text)`)
	mustExec(t, db, `create index ix on t(a, b)`)
	mustExec(t, db, `insert into t values (1, 1, 'x'), (1, 2, 'y'), (2, 1, 'z')`)

	// Confirm index is usable via EXPLAIN QUERY PLAN.
	rows := scanAll(t, db, `explain query plan select c from t where a = 1 and b = 2`)
	plan := ""
	for _, r := range rows {
		if s, ok := r[3].(string); ok {
			plan += " " + s
		}
	}
	if !strings.Contains(plan, "ix") {
		t.Errorf("query plan should use ix: %q", plan)
	}
}

func TestIndex_Partial(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, deleted int default 0, name text)`)
	mustExec(t, db, `create unique index ix_active_name on t(name) where deleted = 0`)

	mustExec(t, db, `insert into t values (1, 0, 'alice')`)
	// Same name allowed if deleted=1 (not in the partial index).
	mustExec(t, db, `insert into t values (2, 1, 'alice')`)
	// But not another deleted=0 alice.
	_, err := db.Exec(`insert into t values (3, 0, 'alice')`)
	if err == nil {
		t.Fatal("expected partial-index unique violation")
	}
}

func TestIndex_Expression(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (name text)`)
	mustExec(t, db, `create index ix_lower on t(lower(name))`)
	mustExec(t, db, `insert into t values ('Alice'), ('ALICE'), ('bob')`)

	// Verify the index is consulted for an expression match.
	rows := scanAll(t, db, `explain query plan select * from t where lower(name) = 'alice'`)
	plan := ""
	for _, r := range rows {
		if s, ok := r[3].(string); ok {
			plan += " " + s
		}
	}
	if !strings.Contains(plan, "ix_lower") {
		t.Errorf("plan should use ix_lower: %q", plan)
	}
}

func TestIndex_Collation(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (name text)`)
	mustExec(t, db, `create index ix_ci on t(name collate nocase)`)
	mustExec(t, db, `insert into t values ('Alice'), ('Bob')`)

	// Confirm NOCASE comparison works (this exercises COLLATE behavior).
	var n int
	scanOne(t, db, &n, `select count(*) from t where name = 'alice' collate nocase`)
	if n != 1 {
		t.Errorf("COLLATE NOCASE match=%d, want 1", n)
	}
}

func TestIndex_IfNotExists(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)
	mustExec(t, db, `create index if not exists ix on t(v)`)
	mustExec(t, db, `create index if not exists ix on t(v)`) // no-op second time
}

func TestIndex_DropAndReindex(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)
	mustExec(t, db, `create index ix on t(v)`)
	mustExec(t, db, `drop index ix`)
	var n int
	scanOne(t, db, &n, `select count(*) from sqlite_master where name='ix'`)
	if n != 0 {
		t.Errorf("after DROP INDEX: %d, want 0", n)
	}

	// REINDEX on a table rebuilds all of its indexes; should be a no-op
	// when there are no indexes left, but must not error.
	mustExec(t, db, `create index ix2 on t(v)`)
	mustExec(t, db, `reindex t`)
}
