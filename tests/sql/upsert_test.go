package sql_test

import "testing"

func TestUpsert_DoNothing(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int primary key, v int)`)
	mustExec(t, db, `insert into t values (1, 100)`)
	mustExec(t, db, `insert into t values (1, 999) on conflict (id) do nothing`)

	var v int64
	scanOne(t, db, &v, `select v from t where id = 1`)
	if v != 100 {
		t.Errorf("after DO NOTHING: v=%d, want 100 (original)", v)
	}
}

func TestUpsert_DoUpdate(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int primary key, v int, hits int default 0)`)
	mustExec(t, db, `insert into t values (1, 100, 0)`)
	mustExec(t, db, `
		insert into t values (1, 200, 0)
		on conflict (id) do update set v = excluded.v, hits = t.hits + 1`)

	var v, hits int64
	scanOne(t, db, &v, `select v from t where id = 1`)
	scanOne(t, db, &hits, `select hits from t where id = 1`)
	if v != 200 || hits != 1 {
		t.Errorf("after DO UPDATE: v=%d hits=%d, want 200, 1", v, hits)
	}
}

func TestUpsert_WhereClause(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int primary key, v int)`)
	mustExec(t, db, `insert into t values (1, 100)`)

	// Conditional update: only update if existing v < new v.
	mustExec(t, db, `
		insert into t values (1, 50)
		on conflict (id) do update set v = excluded.v where t.v < excluded.v`)
	var v int64
	scanOne(t, db, &v, `select v from t where id = 1`)
	if v != 100 {
		t.Errorf("conditional UPSERT: v=%d, want 100 (no update)", v)
	}

	mustExec(t, db, `
		insert into t values (1, 200)
		on conflict (id) do update set v = excluded.v where t.v < excluded.v`)
	scanOne(t, db, &v, `select v from t where id = 1`)
	if v != 200 {
		t.Errorf("conditional UPSERT: v=%d, want 200 (updated)", v)
	}
}

func TestUpsert_MultiRow(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int primary key, v int)`)
	mustExec(t, db, `insert into t values (1, 100), (2, 200)`)
	mustExec(t, db, `
		insert into t values (1, 111), (3, 300)
		on conflict (id) do update set v = excluded.v`)

	rows := scanAll(t, db, `select id, v from t order by id`)
	want := [][]any{
		{int64(1), int64(111)}, // updated
		{int64(2), int64(200)}, // unchanged
		{int64(3), int64(300)}, // inserted
	}
	for i, r := range rows {
		if r[0] != want[i][0] || r[1] != want[i][1] {
			t.Errorf("row %d=%+v, want %+v", i, r, want[i])
		}
	}
}

func TestUpsert_OnConflictNoTarget(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int primary key, v int)`)
	mustExec(t, db, `insert into t values (1, 100)`)
	// Conflict target can be omitted; matches any UNIQUE constraint.
	mustExec(t, db, `insert into t values (1, 200) on conflict do update set v = excluded.v`)
	var v int64
	scanOne(t, db, &v, `select v from t`)
	if v != 200 {
		t.Errorf("v=%d, want 200", v)
	}
}

func TestUpsert_OrIgnore(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int primary key, v int)`)
	mustExec(t, db, `insert into t values (1, 100)`)
	// INSERT OR IGNORE is the older syntax for "skip conflict".
	mustExec(t, db, `insert or ignore into t values (1, 999)`)
	var v int64
	scanOne(t, db, &v, `select v from t`)
	if v != 100 {
		t.Errorf("INSERT OR IGNORE: v=%d, want 100", v)
	}
}
