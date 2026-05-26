package sql_test

import "testing"

func TestView_Basic(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10), (2, 20), (3, 30)`)
	mustExec(t, db, `create view above_15 as select id, v from t where v > 15`)
	rows := scanAll(t, db, `select id from above_15 order by id`)
	if len(rows) != 2 || rows[0][0].(int64) != 2 || rows[1][0].(int64) != 3 {
		t.Errorf("view: %+v, want [2 3]", rows)
	}
}

func TestView_Temporary(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1), (2), (3)`)
	mustExec(t, db, `create temporary view tv as select v * 10 as v10 from t`)
	rows := scanAll(t, db, `select v10 from tv order by v10`)
	if len(rows) != 3 {
		t.Errorf("temp view rows=%d, want 3", len(rows))
	}
}

func TestView_OnView(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10), (2, 20), (3, 30)`)
	mustExec(t, db, `create view v1 as select * from t where v > 10`)
	mustExec(t, db, `create view v2 as select * from v1 where v < 30`)
	rows := scanAll(t, db, `select id from v2`)
	if len(rows) != 1 || rows[0][0].(int64) != 2 {
		t.Errorf("view on view: %+v, want [{2}]", rows)
	}
}

func TestView_DropIfExists(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)
	mustExec(t, db, `create view v as select v from t`)
	mustExec(t, db, `drop view if exists v`)
	// Second call is a no-op.
	mustExec(t, db, `drop view if exists v`)

	var n int
	scanOne(t, db, &n, `select count(*) from sqlite_master where type='view' and name='v'`)
	if n != 0 {
		t.Errorf("after DROP VIEW: %d, want 0", n)
	}
}

func TestView_ColumnAliases(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (a int, b int); insert into t values (1, 2)`)
	// Column-list rename in the CREATE VIEW.
	mustExec(t, db, `create view v (x, y) as select a, b from t`)
	rows, err := db.Query(`select x, y from v`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if cols[0] != "x" || cols[1] != "y" {
		t.Errorf("aliased view cols=%v, want [x y]", cols)
	}
}
