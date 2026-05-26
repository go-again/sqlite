package sql_test

import (
	"strings"
	"testing"
)

func TestTrigger_BeforeInsert(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)
	mustExec(t, db, `create table audit (msg text)`)
	mustExec(t, db, `
		create trigger trg_before_insert before insert on t
		begin
			insert into audit values ('inserting ' || new.v);
		end`)
	mustExec(t, db, `insert into t values (42)`)
	var msg string
	scanOne(t, db, &msg, `select msg from audit`)
	if msg != "inserting 42" {
		t.Errorf("audit msg=%q, want 'inserting 42'", msg)
	}
}

func TestTrigger_AfterUpdate(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10)`)
	mustExec(t, db, `create table audit (id int, old_v int, new_v int)`)
	mustExec(t, db, `
		create trigger trg_after_upd after update of v on t
		begin
			insert into audit values (new.id, old.v, new.v);
		end`)
	mustExec(t, db, `update t set v = 99 where id = 1`)

	rows := scanAll(t, db, `select id, old_v, new_v from audit`)
	if len(rows) != 1 {
		t.Fatalf("audit rows=%d, want 1", len(rows))
	}
	if rows[0][1].(int64) != 10 || rows[0][2].(int64) != 99 {
		t.Errorf("audit row=%+v, want old=10 new=99", rows[0])
	}
}

func TestTrigger_AfterDelete(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int);
		insert into t values (1, 10), (2, 20)`)
	mustExec(t, db, `create table audit (deleted int)`)
	mustExec(t, db, `
		create trigger trg_after_del after delete on t
		begin
			insert into audit values (old.v);
		end`)
	mustExec(t, db, `delete from t where id = 1`)
	var v int64
	scanOne(t, db, &v, `select deleted from audit`)
	if v != 10 {
		t.Errorf("audit deleted=%d, want 10", v)
	}
}

func TestTrigger_InsteadOfView(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, v int)`)
	mustExec(t, db, `create view v_t as select id, v from t`)
	mustExec(t, db, `
		create trigger trg_v_insert instead of insert on v_t
		begin
			insert into t values (new.id, new.v + 1000);
		end`)
	mustExec(t, db, `insert into v_t values (1, 5)`)
	var stored int64
	scanOne(t, db, &stored, `select v from t where id = 1`)
	if stored != 1005 {
		t.Errorf("INSTEAD OF: stored=%d, want 1005", stored)
	}
}

func TestTrigger_WhenClause(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)
	mustExec(t, db, `create table audit (v int)`)
	mustExec(t, db, `
		create trigger trg_pos after insert on t when new.v > 0
		begin
			insert into audit values (new.v);
		end`)
	mustExec(t, db, `insert into t values (-1), (5), (10), (-2)`)

	var n int
	scanOne(t, db, &n, `select count(*) from audit`)
	if n != 2 {
		t.Errorf("audit count=%d, want 2 (only v>0)", n)
	}
}

func TestTrigger_Raise(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)
	mustExec(t, db, `
		create trigger trg_no_zero before insert on t
		begin
			select raise(abort, 'cannot insert zero') where new.v = 0;
		end`)
	mustExec(t, db, `insert into t values (1)`)
	_, err := db.Exec(`insert into t values (0)`)
	if err == nil {
		t.Fatal("expected RAISE(ABORT) to error")
	}
	if !strings.Contains(err.Error(), "cannot insert zero") {
		t.Errorf("error=%v, want contains 'cannot insert zero'", err)
	}
}

func TestTrigger_DropTrigger(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int)`)
	mustExec(t, db, `create trigger trg after insert on t begin select 1; end`)
	mustExec(t, db, `drop trigger trg`)
	var n int
	scanOne(t, db, &n, `select count(*) from sqlite_master where type='trigger' and name='trg'`)
	if n != 0 {
		t.Errorf("after DROP TRIGGER: %d, want 0", n)
	}
}
