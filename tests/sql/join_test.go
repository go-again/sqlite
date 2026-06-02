package sql_test

import (
	"reflect"
	"strings"
	"testing"
)

func TestJoin_InnerJoin(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table emp (id int, name text, dept_id int);
		insert into emp values (1, 'alice', 1), (2, 'bob', 2), (3, 'carol', NULL)`)
	mustExec(t, db, `create table dept (id int, name text);
		insert into dept values (1, 'eng'), (2, 'sales'), (3, 'ops')`)

	rows := scanAll(t, db, `
		select emp.name, dept.name
		from emp inner join dept on emp.dept_id = dept.id
		order by emp.id`)
	want := [][]any{
		{"alice", "eng"},
		{"bob", "sales"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("inner join: %+v, want %+v", rows, want)
	}
}

func TestJoin_LeftJoin(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table emp (id int, name text, dept_id int);
		insert into emp values (1, 'alice', 1), (2, 'bob', 2), (3, 'carol', NULL)`)
	mustExec(t, db, `create table dept (id int, name text);
		insert into dept values (1, 'eng'), (2, 'sales')`)

	rows := scanAll(t, db, `
		select emp.name, dept.name
		from emp left join dept on emp.dept_id = dept.id
		order by emp.id`)
	want := [][]any{
		{"alice", "eng"},
		{"bob", "sales"},
		{"carol", nil},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("left join: %+v, want %+v", rows, want)
	}
}

func TestJoin_LeftJoin_ThreeWay(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table a (id int); insert into a values (1), (2), (3)`)
	mustExec(t, db, `create table b (id int, av int);
		insert into b values (10, 1), (20, 2)`)
	mustExec(t, db, `create table c (id int, bv int);
		insert into c values (100, 10)`)

	rows := scanAll(t, db, `
		select a.id, b.id, c.id
		from a
		left join b on b.av = a.id
		left join c on c.bv = b.id
		order by a.id`)
	want := [][]any{
		{int64(1), int64(10), int64(100)},
		{int64(2), int64(20), nil},
		{int64(3), nil, nil},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("3-way left join: %+v, want %+v", rows, want)
	}
}

func TestJoin_CrossJoin(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table x (v int); insert into x values (1), (2)`)
	mustExec(t, db, `create table y (v int); insert into y values (10), (20)`)

	rows := scanAll(t, db, `select x.v, y.v from x cross join y order by x.v, y.v`)
	want := [][]any{
		{int64(1), int64(10)},
		{int64(1), int64(20)},
		{int64(2), int64(10)},
		{int64(2), int64(20)},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("cross join: %+v, want %+v", rows, want)
	}
}

func TestJoin_Using(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table a (dept_id int, name text);
		insert into a values (1, 'alice'), (2, 'bob')`)
	mustExec(t, db, `create table b (dept_id int, name text);
		insert into b values (1, 'eng'), (2, 'sales')`)

	rows := scanAll(t, db, `select a.name, b.name from a join b using(dept_id) order by dept_id`)
	want := [][]any{
		{"alice", "eng"},
		{"bob", "sales"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("USING join: %+v, want %+v", rows, want)
	}
}

func TestJoin_SelfJoin(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table emp (id int, name text, manager_id int);
		insert into emp values
			(1, 'ceo', NULL),
			(2, 'manager', 1),
			(3, 'worker', 2)`)
	rows := scanAll(t, db, `
		select e.name, m.name as manager
		from emp e left join emp m on m.id = e.manager_id
		order by e.id`)
	want := [][]any{
		{"ceo", nil},
		{"manager", "ceo"},
		{"worker", "manager"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("self-join: %+v, want %+v", rows, want)
	}
}

// TestJoin_RightOuterSupported asserts the bundled SQLite parses
// RIGHT OUTER JOIN. (Older SQLite releases rejected it; the modern
// release we ship supports it, so this test asserts support.)
func TestJoin_RightOuterSupported(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table a (id int); insert into a values (1)`)
	mustExec(t, db, `create table b (id int, av int);
		insert into b values (10, 1), (20, 2)`)

	rows, err := db.Query(`select a.id, b.id from a right join b on b.av = a.id order by b.id`)
	if err != nil {
		// Pre-3.39 SQLite rejects RIGHT JOIN; document that.
		if strings.Contains(err.Error(), "RIGHT and FULL OUTER JOINs are not currently supported") {
			t.Skipf("SQLite < 3.39 does not support RIGHT JOIN: %v", err)
		}
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][]any
	for rows.Next() {
		var av, bv any
		if err := rows.Scan(&av, &bv); err != nil {
			t.Fatal(err)
		}
		got = append(got, []any{av, bv})
	}
	want := [][]any{
		{int64(1), int64(10)},
		{nil, int64(20)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RIGHT JOIN: %+v, want %+v", got, want)
	}
}
