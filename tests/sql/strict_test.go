package sql_test

import (
	"strings"
	"testing"
)

func TestStrict_BasicTypeEnforcement(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.37" {
		t.Skipf("STRICT tables require SQLite >= 3.37, have %s", v)
	}
	mustExec(t, db, `create table t (
		i integer,
		r real,
		s text,
		b blob
	) strict`)

	// Valid inserts.
	mustExec(t, db, `insert into t values (1, 1.5, 'hi', x'01')`)

	// Strict mode rejects type mismatches that loose mode would coerce.
	_, err := db.Exec(`insert into t (i) values ('not a number')`)
	if err == nil {
		t.Fatal("expected STRICT to reject non-numeric in INTEGER column")
	}
	// Accept either the "TYPE" diagnostic class or the more specific
	// "cannot store TEXT value in INTEGER column" message.
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "type") && !strings.Contains(msg, "cannot store") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStrict_AnyTypeColumn(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.37" {
		t.Skipf("STRICT tables require SQLite >= 3.37, have %s", v)
	}
	// ANY columns accept any storage class in STRICT mode.
	mustExec(t, db, `create table t (v any) strict`)
	mustExec(t, db, `insert into t values (42), (1.5), ('text'), (x'00'), (NULL)`)
	var n int
	scanOne(t, db, &n, `select count(*) from t`)
	if n != 5 {
		t.Errorf("ANY column count=%d, want 5", n)
	}
}

func TestStrict_IntPreservesType(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.37" {
		t.Skipf("STRICT tables require SQLite >= 3.37, have %s", v)
	}
	mustExec(t, db, `create table t (i integer) strict`)
	mustExec(t, db, `insert into t values (42)`)
	var typ string
	scanOne(t, db, &typ, `select typeof(i) from t`)
	// In STRICT, the type matches the declaration — INTEGER stays INTEGER.
	if typ != "integer" {
		t.Errorf("STRICT typeof=%q, want integer", typ)
	}
}

func TestStrict_WithoutRowidCombined(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.37" {
		t.Skipf("STRICT tables require SQLite >= 3.37, have %s", v)
	}
	mustExec(t, db, `create table t (
		id integer primary key,
		v text
	) strict, without rowid`)
	mustExec(t, db, `insert into t values (1, 'a'), (2, 'b')`)

	var n int
	scanOne(t, db, &n, `select count(*) from t`)
	if n != 2 {
		t.Errorf("STRICT+WITHOUT ROWID count=%d, want 2", n)
	}
}
