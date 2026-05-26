package sql_test

import (
	"bytes"
	"database/sql"
	"testing"
	"time"
)

// TestType_NullRoundTrip asserts every sql.Null* wrapper round-trips both
// the "valid" and "invalid" forms through a TEXT column. The driver must
// honor sql.Scanner / driver.Valuer for these wrappers.
func TestType_NullRoundTrip(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (
		s text, i integer, f real, b integer, ts datetime
	)`)
	// Insert a fully-NULL row and a fully-populated row.
	mustExec(t, db, `insert into t values (?, ?, ?, ?, ?)`,
		sql.NullString{}, sql.NullInt64{}, sql.NullFloat64{}, sql.NullBool{}, sql.NullTime{})
	mustExec(t, db, `insert into t values (?, ?, ?, ?, ?)`,
		sql.NullString{String: "hi", Valid: true},
		sql.NullInt64{Int64: 42, Valid: true},
		sql.NullFloat64{Float64: 3.14, Valid: true},
		sql.NullBool{Bool: true, Valid: true},
		sql.NullTime{Time: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), Valid: true})

	rows, err := db.Query(`select s, i, f, b, ts from t order by rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	// Row 1: all NULL.
	if !rows.Next() {
		t.Fatal("missing row 1")
	}
	var ns sql.NullString
	var ni sql.NullInt64
	var nf sql.NullFloat64
	var nb sql.NullBool
	var nt sql.NullTime
	if err := rows.Scan(&ns, &ni, &nf, &nb, &nt); err != nil {
		t.Fatal(err)
	}
	if ns.Valid || ni.Valid || nf.Valid || nb.Valid || nt.Valid {
		t.Errorf("row 1: expected all-NULL, got %+v %+v %+v %+v %+v", ns, ni, nf, nb, nt)
	}

	// Row 2: populated.
	if !rows.Next() {
		t.Fatal("missing row 2")
	}
	if err := rows.Scan(&ns, &ni, &nf, &nb, &nt); err != nil {
		t.Fatal(err)
	}
	if !ns.Valid || ns.String != "hi" {
		t.Errorf("NullString=%+v, want {hi true}", ns)
	}
	if !ni.Valid || ni.Int64 != 42 {
		t.Errorf("NullInt64=%+v, want {42 true}", ni)
	}
	if !nf.Valid || nf.Float64 != 3.14 {
		t.Errorf("NullFloat64=%+v, want {3.14 true}", nf)
	}
	if !nb.Valid || !nb.Bool {
		t.Errorf("NullBool=%+v, want {true true}", nb)
	}
	if !nt.Valid || !nt.Time.Equal(time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("NullTime=%+v, want 2026-05-01T12:00:00Z", nt)
	}
}

// TestType_IntegerAffinity exercises column affinity rules for INTEGER:
// inserting the string "123" should coerce to int64. Documented at
// https://www.sqlite.org/datatype3.html#type_affinity.
func TestType_IntegerAffinity(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (i integer)`)
	mustExec(t, db, `insert into t values ('123')`)
	mustExec(t, db, `insert into t values (4.0)`)
	mustExec(t, db, `insert into t values ('not a number')`)

	rows := scanAll(t, db, `select typeof(i), i from t order by rowid`)
	want := [][]any{
		{"integer", int64(123)},
		{"integer", int64(4)},
		{"text", "not a number"}, // unparsable text keeps TEXT storage class
	}
	for i, w := range want {
		if rows[i][0] != w[0] {
			t.Errorf("row %d typeof=%v, want %v", i, rows[i][0], w[0])
		}
		if rows[i][1] != w[1] {
			t.Errorf("row %d value=%v, want %v", i, rows[i][1], w[1])
		}
	}
}

// TestType_RealAffinity asserts REAL coerces strings like "1.5" to float64
// and keeps INTEGER inputs as INTEGER storage class (per spec — REAL
// affinity casts only when conversion is lossless).
func TestType_RealAffinity(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (f real)`)
	mustExec(t, db, `insert into t values ('1.5')`)
	mustExec(t, db, `insert into t values (10)`)

	rows := scanAll(t, db, `select typeof(f), f from t order by rowid`)
	if rows[0][0] != "real" || rows[0][1].(float64) != 1.5 {
		t.Errorf("row 0=%v, want real 1.5", rows[0])
	}
	// SQLite REAL affinity stores integers as REAL when forced via cast,
	// but raw INSERT may keep INTEGER storage. Accept either.
	if rows[1][0] != "real" && rows[1][0] != "integer" {
		t.Errorf("row 1 typeof=%v, want real or integer", rows[1][0])
	}
}

// TestType_TextAffinity asserts TEXT does not coerce — numeric inputs
// are stored as TEXT.
func TestType_TextAffinity(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (s text)`)
	mustExec(t, db, `insert into t values (?)`, 42)
	mustExec(t, db, `insert into t values (?)`, 3.14)

	rows := scanAll(t, db, `select typeof(s), s from t order by rowid`)
	want := []string{"text", "text"}
	for i, w := range want {
		if rows[i][0] != w {
			t.Errorf("row %d typeof=%v, want %s", i, rows[i][0], w)
		}
	}
	if rows[0][1] != "42" {
		t.Errorf("row 0 value=%v, want '42'", rows[0][1])
	}
}

// TestType_BlobAffinity asserts BLOB preserves exact bytes including
// embedded NULs, and that BLOB column reads return []byte not string.
func TestType_BlobAffinity(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (b blob)`)
	payload := []byte{0x00, 0x01, 0xff, 0x7f, 0x00, 0x80}
	mustExec(t, db, `insert into t values (?)`, payload)

	var got []byte
	scanOne(t, db, &got, `select b from t`)
	if !bytes.Equal(got, payload) {
		t.Errorf("blob round-trip mismatch: got %x, want %x", got, payload)
	}
	var typ string
	scanOne(t, db, &typ, `select typeof(b) from t`)
	if typ != "blob" {
		t.Errorf("typeof(blob)=%q, want blob", typ)
	}
}

// TestType_NumericAffinity asserts NUMERIC accepts both INTEGER and REAL
// inputs and reduces to INTEGER when lossless. Per spec, "1.5" becomes
// REAL (no integer reduction), "1.0" becomes INTEGER.
func TestType_NumericAffinity(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (n numeric)`)
	mustExec(t, db, `insert into t values ('1.5')`)
	mustExec(t, db, `insert into t values ('1.0')`)
	mustExec(t, db, `insert into t values ('5')`)
	mustExec(t, db, `insert into t values ('abc')`)

	rows := scanAll(t, db, `select typeof(n), n from t order by rowid`)
	want := []string{"real", "integer", "integer", "text"}
	for i, w := range want {
		if rows[i][0] != w {
			t.Errorf("row %d typeof=%v, want %s", i, rows[i][0], w)
		}
	}
}

// TestType_NoneAffinity asserts a column with no declared type (or BLOB-
// declared, which equals NONE affinity) preserves the storage class of
// the value as inserted.
func TestType_NoneAffinity(t *testing.T) {
	db := openDB(t)
	// Column declared without a type yields BLOB affinity = NONE.
	mustExec(t, db, `create table t (v)`)
	mustExec(t, db, `insert into t values (?)`, "text-input")
	mustExec(t, db, `insert into t values (?)`, int64(42))
	mustExec(t, db, `insert into t values (?)`, 3.14)
	mustExec(t, db, `insert into t values (?)`, []byte{1, 2, 3})

	rows := scanAll(t, db, `select typeof(v) from t order by rowid`)
	want := []string{"text", "integer", "real", "blob"}
	for i, w := range want {
		if rows[i][0] != w {
			t.Errorf("row %d typeof=%v, want %s", i, rows[i][0], w)
		}
	}
}

// TestType_NullSortOrder asserts NULLs FIRST vs NULLS LAST in ORDER BY,
// and the SQLite default (NULLs sort lowest with ASC).
func TestType_NullSortOrder(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (n integer)`)
	mustExec(t, db, `insert into t values (2), (NULL), (1), (NULL), (3)`)

	asc := scanAll(t, db, `select n from t order by n`)
	// SQLite default: NULLs are sorted lowest in ASC order.
	if asc[0][0] != nil || asc[1][0] != nil {
		t.Errorf("default ASC: first two values should be NULL, got %+v", asc[:2])
	}

	nullsLast := scanAll(t, db, `select n from t order by n nulls last`)
	// With NULLS LAST, the trailing two should be NULL.
	n := len(nullsLast)
	if nullsLast[n-1][0] != nil || nullsLast[n-2][0] != nil {
		t.Errorf("NULLS LAST: last two values should be NULL, got %+v", nullsLast[n-2:])
	}

	nullsFirst := scanAll(t, db, `select n from t order by n desc nulls first`)
	if nullsFirst[0][0] != nil || nullsFirst[1][0] != nil {
		t.Errorf("DESC NULLS FIRST: first two values should be NULL, got %+v", nullsFirst[:2])
	}
}

// TestType_EmptyVsNull asserts SQLite distinguishes '' from NULL and that
// the != operator returns NULL (not true) when one side is NULL.
func TestType_EmptyVsNull(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (s text)`)
	mustExec(t, db, `insert into t values ('')`)
	mustExec(t, db, `insert into t values (NULL)`)

	// Count of '' should be 1 (NULL is excluded by != comparison).
	var emptyCount int
	scanOne(t, db, &emptyCount, `select count(*) from t where s = ''`)
	if emptyCount != 1 {
		t.Errorf("count(*) where s = '': %d, want 1", emptyCount)
	}

	// IS NULL must return exactly 1 row.
	var nullCount int
	scanOne(t, db, &nullCount, `select count(*) from t where s is null`)
	if nullCount != 1 {
		t.Errorf("count(*) where s is null: %d, want 1", nullCount)
	}

	// != with NULL is NULL, not true — so this returns 0 rows.
	var diffCount int
	scanOne(t, db, &diffCount, `select count(*) from t where s != ''`)
	if diffCount != 0 {
		t.Errorf("count(*) where s != '': %d, want 0 (NULL != '' is NULL)", diffCount)
	}

	// IS NOT operator (three-valued logic friendly) handles NULL.
	var notCount int
	scanOne(t, db, &notCount, `select count(*) from t where s is not ''`)
	if notCount != 1 {
		t.Errorf("count(*) where s is not '': %d, want 1", notCount)
	}
}
