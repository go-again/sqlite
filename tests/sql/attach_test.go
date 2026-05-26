package sql_test

import "testing"

func TestAttach_BasicAttachAndQuery(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1), (2)`)
	mustExec(t, db, `attach database ':memory:' as aux`)
	mustExec(t, db, `create table aux.t2 (v int); insert into aux.t2 values (10), (20)`)

	rows := scanAll(t, db, `select v from aux.t2 order by v`)
	if len(rows) != 2 || rows[0][0].(int64) != 10 {
		t.Errorf("attached query: %+v, want [10 20]", rows)
	}
}

func TestAttach_CrossDbJoin(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table main_t (id int, label text);
		insert into main_t values (1, 'a'), (2, 'b')`)
	mustExec(t, db, `attach database ':memory:' as aux`)
	mustExec(t, db, `create table aux.aux_t (id int, score int);
		insert into aux.aux_t values (1, 100), (2, 200)`)

	rows := scanAll(t, db, `
		select main_t.label, aux.aux_t.score
		from main_t join aux.aux_t on main_t.id = aux.aux_t.id
		order by main_t.id`)
	if len(rows) != 2 || rows[0][0] != "a" || rows[0][1].(int64) != 100 {
		t.Errorf("cross-db join: %+v", rows)
	}
}

func TestAttach_Detach(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `attach database ':memory:' as aux`)
	mustExec(t, db, `create table aux.t (v int); insert into aux.t values (1)`)
	mustExec(t, db, `detach database aux`)

	_, err := db.Exec(`select * from aux.t`)
	if err == nil {
		t.Fatal("expected error after detach")
	}
}

func TestAttach_DatabaseList(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `attach database ':memory:' as aux`)
	rows := scanAll(t, db, `pragma database_list`)
	// Should have at least main + aux.
	names := map[string]bool{}
	for _, r := range rows {
		if name, ok := r[1].(string); ok {
			names[name] = true
		}
	}
	if !names["main"] || !names["aux"] {
		t.Errorf("database_list: %+v, want main+aux", names)
	}
}
