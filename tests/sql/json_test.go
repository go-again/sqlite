package sql_test

import (
	"reflect"
	"strings"
	"testing"
)

func TestJSON_Validate(t *testing.T) {
	db := openDB(t)
	var valid int
	scanOne(t, db, &valid, `select json_valid('{"a":1}')`)
	if valid != 1 {
		t.Errorf("json_valid(good)=%d, want 1", valid)
	}
	scanOne(t, db, &valid, `select json_valid('{not json')`)
	if valid != 0 {
		t.Errorf("json_valid(bad)=%d, want 0", valid)
	}
}

func TestJSON_Extract(t *testing.T) {
	db := openDB(t)
	cases := []struct {
		sql  string
		want any
	}{
		{`select json_extract('{"a":1,"b":"two"}', '$.a')`, int64(1)},
		{`select json_extract('{"a":1,"b":"two"}', '$.b')`, "two"},
		{`select json_extract('{"a":[10,20,30]}', '$.a[1]')`, int64(20)},
		{`select json_extract('{"nested":{"x":42}}', '$.nested.x')`, int64(42)},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			var got any
			scanOne(t, db, &got, c.sql)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s = %v (%T), want %v (%T)", c.sql, got, got, c.want, c.want)
			}
		})
	}
}

func TestJSON_Operators_ArrowDoubleArrow(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.38" {
		t.Skipf("-> and ->> operators require SQLite >= 3.38, have %s", v)
	}
	// -> returns JSON (still a json-encoded string in TEXT form)
	// ->> returns the SQL native value (unwrapped).
	var sqlText, jsonText string
	scanOne(t, db, &jsonText, `select '{"x":42}' -> '$.x'`)
	scanOne(t, db, &sqlText, `select '{"x":42}' ->> '$.x'`)
	if jsonText != "42" {
		t.Errorf("-> = %q, want '42'", jsonText)
	}
	if sqlText != "42" {
		t.Errorf("->> = %q, want '42'", sqlText)
	}
	// String values: -> wraps in quotes, ->> doesn't.
	scanOne(t, db, &jsonText, `select '{"x":"hi"}' -> '$.x'`)
	scanOne(t, db, &sqlText, `select '{"x":"hi"}' ->> '$.x'`)
	if jsonText != `"hi"` {
		t.Errorf("-> string = %q, want '\"hi\"'", jsonText)
	}
	if sqlText != "hi" {
		t.Errorf("->> string = %q, want 'hi'", sqlText)
	}
}

func TestJSON_Set(t *testing.T) {
	db := openDB(t)
	var got string
	scanOne(t, db, &got, `select json_set('{"a":1}', '$.a', 99, '$.b', 'new')`)
	// json_set sets both, regardless of pre-existence.
	if !strings.Contains(got, `"a":99`) || !strings.Contains(got, `"b":"new"`) {
		t.Errorf("json_set=%q, want a:99 and b:new", got)
	}
}

func TestJSON_InsertVsReplace(t *testing.T) {
	db := openDB(t)
	// json_insert sets only if not present; json_replace sets only if present.
	var inserted, replaced string
	scanOne(t, db, &inserted, `select json_insert('{"a":1}', '$.a', 99, '$.b', 'new')`)
	scanOne(t, db, &replaced, `select json_replace('{"a":1}', '$.a', 99, '$.b', 'new')`)
	// json_insert: a already exists (stays 1), b is added.
	if !strings.Contains(inserted, `"a":1`) || !strings.Contains(inserted, `"b":"new"`) {
		t.Errorf("json_insert=%q", inserted)
	}
	// json_replace: a is replaced, b doesn't exist so not added.
	if !strings.Contains(replaced, `"a":99`) || strings.Contains(replaced, `"b"`) {
		t.Errorf("json_replace=%q", replaced)
	}
}

func TestJSON_Remove(t *testing.T) {
	db := openDB(t)
	var got string
	scanOne(t, db, &got, `select json_remove('{"a":1,"b":2,"c":3}', '$.b')`)
	if strings.Contains(got, "b") {
		t.Errorf("json_remove=%q, want no 'b' key", got)
	}
}

func TestJSON_ArrayObject(t *testing.T) {
	db := openDB(t)
	var arr, obj string
	scanOne(t, db, &arr, `select json_array(1, 'two', 3.0)`)
	if arr != `[1,"two",3.0]` {
		t.Errorf("json_array=%q", arr)
	}
	scanOne(t, db, &obj, `select json_object('a', 1, 'b', 'two')`)
	if !strings.Contains(obj, `"a":1`) || !strings.Contains(obj, `"b":"two"`) {
		t.Errorf("json_object=%q", obj)
	}
}

func TestJSON_Each(t *testing.T) {
	db := openDB(t)
	rows := scanAll(t, db, `select key, value from json_each('[10,20,30]')`)
	if len(rows) != 3 {
		t.Fatalf("rows=%d, want 3", len(rows))
	}
	for i, r := range rows {
		if r[0].(int64) != int64(i) {
			t.Errorf("row %d key=%v, want %d", i, r[0], i)
		}
	}
}

func TestJSON_Tree(t *testing.T) {
	db := openDB(t)
	rows := scanAll(t, db, `select type, path from json_tree('{"a":[1,2]}') where type != 'object'`)
	if len(rows) == 0 {
		t.Fatal("json_tree returned no non-object rows")
	}
}

func TestJSON_GroupArray(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (v int); insert into t values (1), (2), (3)`)
	var got string
	scanOne(t, db, &got, `select json_group_array(v) from t`)
	if got != "[1,2,3]" {
		t.Errorf("json_group_array=%q, want [1,2,3]", got)
	}
}

func TestJSON_GroupObject(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (k text, v int);
		insert into t values ('a', 1), ('b', 2)`)
	var got string
	scanOne(t, db, &got, `select json_group_object(k, v) from t`)
	if !strings.Contains(got, `"a":1`) || !strings.Contains(got, `"b":2`) {
		t.Errorf("json_group_object=%q", got)
	}
}

func TestJSON_TypeFunction(t *testing.T) {
	db := openDB(t)
	cases := []struct {
		sql, want string
	}{
		{`select json_type('null')`, "null"},
		{`select json_type('true')`, "true"},
		{`select json_type('42')`, "integer"},
		{`select json_type('1.5')`, "real"},
		{`select json_type('"hi"')`, "text"},
		{`select json_type('[1,2]')`, "array"},
		{`select json_type('{"a":1}')`, "object"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			var got string
			scanOne(t, db, &got, c.sql)
			if got != c.want {
				t.Errorf("%s = %q, want %q", c.sql, got, c.want)
			}
		})
	}
}

func TestJSON_Quote(t *testing.T) {
	db := openDB(t)
	var got string
	scanOne(t, db, &got, `select json_quote('hello')`)
	if got != `"hello"` {
		t.Errorf("json_quote=%q, want '\"hello\"'", got)
	}
}
