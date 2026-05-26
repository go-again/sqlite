package sql_test

import (
	"reflect"
	"testing"
)

func TestSubquery_Scalar(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (5), (10), (15)`)

	// Scalar subquery in SELECT projection.
	var max, total int64
	scanOne(t, db, &max, `select (select max(v) from t)`)
	scanOne(t, db, &total, `select (select sum(v) from t)`)
	if max != 15 || total != 30 {
		t.Errorf("scalar subquery: max=%d total=%d, want 15, 30", max, total)
	}

	// Scalar subquery in WHERE: rows above the average.
	rows := scanAll(t, db, `
		select v from t where v > (select avg(v) from t) order by v`)
	want := [][]any{{int64(15)}}
	if rows[0][0].(int64) != want[0][0].(int64) || len(rows) != 1 {
		t.Errorf("scalar subquery in WHERE: %+v, want %+v", rows, want)
	}
}

func TestSubquery_Correlated(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table emp (id int, dept text, salary int);
		insert into emp values
			(1, 'a', 100),
			(2, 'a', 200),
			(3, 'b', 50),
			(4, 'b', 75)`)

	// "salary above average for own department"
	rows := scanAll(t, db, `
		select id from emp e
		where salary > (select avg(salary) from emp where dept = e.dept)
		order by id`)
	want := [][]any{{int64(2)}, {int64(4)}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("correlated subquery: %+v, want %+v", rows, want)
	}
}

func TestSubquery_Exists(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table a (id int); insert into a values (1), (2), (3)`)
	mustExec(t, db, `create table b (av int); insert into b values (2), (3)`)

	hasMatch := scanAll(t, db, `
		select id from a where exists (select 1 from b where b.av = a.id)
		order by id`)
	want := [][]any{{int64(2)}, {int64(3)}}
	if !reflect.DeepEqual(hasMatch, want) {
		t.Errorf("EXISTS: %+v, want %+v", hasMatch, want)
	}

	noMatch := scanAll(t, db, `
		select id from a where not exists (select 1 from b where b.av = a.id)
		order by id`)
	wantN := [][]any{{int64(1)}}
	if !reflect.DeepEqual(noMatch, wantN) {
		t.Errorf("NOT EXISTS: %+v, want %+v", noMatch, wantN)
	}
}

func TestSubquery_InSubquery(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table a (id int, label text);
		insert into a values (1, 'red'), (2, 'green'), (3, 'blue')`)
	mustExec(t, db, `create table colors (name text);
		insert into colors values ('red'), ('blue')`)

	rows := scanAll(t, db, `
		select id from a where label in (select name from colors) order by id`)
	want := [][]any{{int64(1)}, {int64(3)}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("IN subquery: %+v, want %+v", rows, want)
	}
}
