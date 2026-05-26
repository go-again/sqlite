package sql_test

import "testing"

func TestReturning_Insert(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.35" {
		t.Skipf("RETURNING requires SQLite >= 3.35, have %s", v)
	}
	mustExec(t, db, `create table t (id integer primary key, v text)`)
	rows, err := db.Query(`insert into t (v) values ('a'), ('b') returning id, v`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []struct {
		id int64
		v  string
	}
	for rows.Next() {
		var r struct {
			id int64
			v  string
		}
		if err := rows.Scan(&r.id, &r.v); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 || got[0].id != 1 || got[1].v != "b" {
		t.Errorf("RETURNING: %+v", got)
	}
}

func TestReturning_Update(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.35" {
		t.Skipf("RETURNING requires SQLite >= 3.35, have %s", v)
	}
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10), (2, 20)`)
	rows := scanAll(t, db, `update t set v = v + 1 where id = 1 returning v`)
	if len(rows) != 1 || rows[0][0].(int64) != 11 {
		t.Errorf("UPDATE RETURNING: %+v, want [{11}]", rows)
	}
}

func TestReturning_Delete(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.35" {
		t.Skipf("RETURNING requires SQLite >= 3.35, have %s", v)
	}
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10), (2, 20)`)
	rows := scanAll(t, db, `delete from t where id = 1 returning *`)
	if len(rows) != 1 {
		t.Errorf("DELETE RETURNING rows=%d, want 1", len(rows))
	}
}

func TestReturning_Star(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.35" {
		t.Skipf("RETURNING requires SQLite >= 3.35, have %s", v)
	}
	mustExec(t, db, `create table t (a int, b int)`)
	rows := scanAll(t, db, `insert into t values (1, 2) returning *`)
	if len(rows) != 1 || rows[0][0].(int64) != 1 || rows[0][1].(int64) != 2 {
		t.Errorf("RETURNING *: %+v, want [{1 2}]", rows)
	}
}

func TestReturning_WithAlias(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.35" {
		t.Skipf("RETURNING requires SQLite >= 3.35, have %s", v)
	}
	mustExec(t, db, `create table t (v int)`)
	rows, err := db.Query(`insert into t values (5) returning v * 2 as doubled`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if cols[0] != "doubled" {
		t.Errorf("alias=%v, want doubled", cols)
	}
	if !rows.Next() {
		t.Fatal("no rows")
	}
	var v int64
	if err := rows.Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 10 {
		t.Errorf("v=%d, want 10", v)
	}
}
