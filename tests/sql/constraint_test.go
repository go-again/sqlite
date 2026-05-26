package sql_test

import (
	"strings"
	"testing"
)

func TestConstraint_IntegerPrimaryKey(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id integer primary key, v text)`)
	mustExec(t, db, `insert into t (v) values ('a'), ('b')`)
	rows := scanAll(t, db, `select id, v from t order by id`)
	// INTEGER PRIMARY KEY is an alias for ROWID — auto-assigned 1, 2.
	if rows[0][0].(int64) != 1 || rows[1][0].(int64) != 2 {
		t.Errorf("INTEGER PK: %+v, want id=1,2", rows)
	}
}

func TestConstraint_AutoIncrement(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id integer primary key autoincrement, v text)`)
	mustExec(t, db, `insert into t (v) values ('a')`)
	mustExec(t, db, `delete from t`)
	mustExec(t, db, `insert into t (v) values ('b')`)
	var id int64
	scanOne(t, db, &id, `select id from t`)
	// With AUTOINCREMENT, deleted rowids are NOT reused, so the second
	// insert gets id 2, not id 1.
	if id != 2 {
		t.Errorf("AUTOINCREMENT after delete: id=%d, want 2", id)
	}
}

func TestConstraint_CompositePrimaryKey(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (a int, b int, primary key (a, b))`)
	mustExec(t, db, `insert into t values (1, 1), (1, 2), (2, 1)`)
	_, err := db.Exec(`insert into t values (1, 1)`)
	if err == nil {
		t.Fatal("expected unique-violation error on duplicate composite PK")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("error %v doesn't mention UNIQUE", err)
	}
}

func TestConstraint_Unique(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (id int, email text unique)`)
	mustExec(t, db, `insert into t values (1, 'a@x'), (2, 'b@x')`)
	_, err := db.Exec(`insert into t values (3, 'a@x')`)
	if err == nil {
		t.Fatal("expected unique violation")
	}
	// SQLite allows multiple NULLs in a UNIQUE column.
	mustExec(t, db, `insert into t values (4, NULL), (5, NULL)`)
}

func TestConstraint_NotNull(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v text not null)`)
	_, err := db.Exec(`insert into t values (NULL)`)
	if err == nil {
		t.Fatal("expected NOT NULL violation")
	}
}

func TestConstraint_Check(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (
		age int check (age >= 0 and age < 200),
		email text check (email like '%@%')
	)`)
	mustExec(t, db, `insert into t values (30, 'a@b')`)
	_, err := db.Exec(`insert into t values (-1, 'a@b')`)
	if err == nil {
		t.Fatal("expected CHECK violation on age")
	}
	_, err = db.Exec(`insert into t values (30, 'no-at-sign')`)
	if err == nil {
		t.Fatal("expected CHECK violation on email")
	}
}

func TestConstraint_ForeignKey_Cascade(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `pragma foreign_keys = ON`)
	mustExec(t, db, `create table parent (id int primary key)`)
	mustExec(t, db, `create table child (id int, p_id int,
		foreign key (p_id) references parent(id) on delete cascade on update cascade)`)
	mustExec(t, db, `insert into parent values (1), (2)`)
	mustExec(t, db, `insert into child values (10, 1), (11, 1), (12, 2)`)

	mustExec(t, db, `delete from parent where id = 1`)
	var n int
	scanOne(t, db, &n, `select count(*) from child where p_id = 1`)
	if n != 0 {
		t.Errorf("after CASCADE delete: %d child rows remain, want 0", n)
	}

	mustExec(t, db, `update parent set id = 99 where id = 2`)
	scanOne(t, db, &n, `select count(*) from child where p_id = 99`)
	if n != 1 {
		t.Errorf("after CASCADE update: %d child rows match new id, want 1", n)
	}
}

func TestConstraint_ForeignKey_Restrict(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `pragma foreign_keys = ON`)
	mustExec(t, db, `create table parent (id int primary key)`)
	mustExec(t, db, `create table child (id int, p_id int,
		foreign key (p_id) references parent(id) on delete restrict)`)
	mustExec(t, db, `insert into parent values (1)`)
	mustExec(t, db, `insert into child values (10, 1)`)
	_, err := db.Exec(`delete from parent where id = 1`)
	if err == nil {
		t.Fatal("expected FK violation on restricted parent delete")
	}
}

func TestConstraint_ForeignKey_SetNull(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `pragma foreign_keys = ON`)
	mustExec(t, db, `create table parent (id int primary key)`)
	mustExec(t, db, `create table child (id int, p_id int,
		foreign key (p_id) references parent(id) on delete set null)`)
	mustExec(t, db, `insert into parent values (1)`)
	mustExec(t, db, `insert into child values (10, 1)`)
	mustExec(t, db, `delete from parent where id = 1`)
	var v any
	scanOne(t, db, &v, `select p_id from child where id = 10`)
	if v != nil {
		t.Errorf("SET NULL: p_id=%v, want nil", v)
	}
}

func TestConstraint_Default(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (
		a text default 'hello',
		b int default 42,
		c text default (lower('UPPER'))
	)`)
	mustExec(t, db, `insert into t default values`)
	var a, c string
	var b int
	scanOne(t, db, &a, `select a from t`)
	scanOne(t, db, &b, `select b from t`)
	scanOne(t, db, &c, `select c from t`)
	if a != "hello" || b != 42 || c != "upper" {
		t.Errorf("DEFAULTs: a=%q b=%d c=%q, want hello, 42, upper", a, b, c)
	}
}

func TestConstraint_DeferredForeignKey(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `pragma foreign_keys = ON`)
	mustExec(t, db, `create table parent (id int primary key)`)
	mustExec(t, db, `create table child (id int, p_id int,
		foreign key (p_id) references parent(id) deferrable initially deferred)`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Insert child first, parent second — allowed because the FK check
	// is deferred to COMMIT.
	if _, err := tx.Exec(`insert into child values (10, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`insert into parent values (1)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit should succeed (both rows present): %v", err)
	}
}
