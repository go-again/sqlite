package sql_test

import "testing"

func TestCond_CaseWhen_Simple(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1), (2), (3), (4)`)
	rows := scanAll(t, db, `
		select v, case when v % 2 = 0 then 'even' else 'odd' end as parity
		from t order by v`)
	want := []string{"odd", "even", "odd", "even"}
	for i, r := range rows {
		if r[1].(string) != want[i] {
			t.Errorf("v=%v parity=%v, want %s", r[0], r[1], want[i])
		}
	}
}

func TestCond_CaseWhen_Searched(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (5), (15), (25)`)
	rows := scanAll(t, db, `
		select v,
		       case v
		            when 5 then 'five'
		            when 15 then 'fifteen'
		            else 'other'
		       end as label
		from t order by v`)
	want := []string{"five", "fifteen", "other"}
	for i, r := range rows {
		if r[1].(string) != want[i] {
			t.Errorf("v=%v label=%v, want %s", r[0], r[1], want[i])
		}
	}
}

func TestCond_Iif(t *testing.T) {
	db := openDB(t)
	var s string
	scanOne(t, db, &s, `select iif(1 < 2, 'yes', 'no')`)
	if s != "yes" {
		t.Errorf("iif=%q, want 'yes'", s)
	}
	scanOne(t, db, &s, `select iif(1 > 2, 'yes', 'no')`)
	if s != "no" {
		t.Errorf("iif=%q, want 'no'", s)
	}
}

func TestCond_Coalesce(t *testing.T) {
	db := openDB(t)
	var s string
	scanOne(t, db, &s, `select coalesce(null, null, 'x', 'y')`)
	if s != "x" {
		t.Errorf("coalesce=%q, want 'x'", s)
	}
}

func TestCond_Ifnull(t *testing.T) {
	db := openDB(t)
	var s string
	scanOne(t, db, &s, `select ifnull(null, 'default')`)
	if s != "default" {
		t.Errorf("ifnull(null)=%q, want 'default'", s)
	}
	scanOne(t, db, &s, `select ifnull('value', 'default')`)
	if s != "value" {
		t.Errorf("ifnull(value)=%q, want 'value'", s)
	}
}

func TestCond_Nullif(t *testing.T) {
	db := openDB(t)
	// nullif(a, b) returns NULL when a == b, else returns a.
	var v any
	scanOne(t, db, &v, `select nullif('x', 'x')`)
	if v != nil {
		t.Errorf("nullif equal=%v, want nil", v)
	}
	scanOne(t, db, &v, `select nullif('x', 'y')`)
	if v != "x" {
		t.Errorf("nullif differ=%v, want 'x'", v)
	}
}
