package sql_test

import (
	"strings"
	"testing"
)

func TestStr_LengthAndCharIndex(t *testing.T) {
	db := openDB(t)
	var n int
	scanOne(t, db, &n, `select length('hello')`)
	if n != 5 {
		t.Errorf("length('hello')=%d, want 5", n)
	}
	// SQLite length() is UTF-8 character count, not byte count.
	scanOne(t, db, &n, `select length('café')`)
	if n != 4 {
		t.Errorf("length('café')=%d, want 4", n)
	}
}

func TestStr_SubstrOffsets(t *testing.T) {
	db := openDB(t)
	cases := []struct {
		sql  string
		want string
	}{
		{`select substr('abcdef', 1, 3)`, "abc"},
		{`select substr('abcdef', 2, 3)`, "bcd"},
		{`select substr('abcdef', 4)`, "def"},
		{`select substr('abcdef', -2)`, "ef"}, // negative offset from end
		{`select substr('abcdef', -3, 2)`, "de"},
		{`select substring('abcdef', 1, 2)`, "ab"}, // SQL standard alias
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

func TestStr_Replace(t *testing.T) {
	db := openDB(t)
	var got string
	scanOne(t, db, &got, `select replace('hello world', 'world', 'sql')`)
	if got != "hello sql" {
		t.Errorf("replace=%q, want 'hello sql'", got)
	}
	scanOne(t, db, &got, `select replace('aaabaa', 'a', 'X')`)
	if got != "XXXbXX" {
		t.Errorf("replace=%q, want 'XXXbXX'", got)
	}
}

func TestStr_Instr(t *testing.T) {
	db := openDB(t)
	var pos int
	scanOne(t, db, &pos, `select instr('abcdef', 'cd')`)
	if pos != 3 {
		t.Errorf("instr=%d, want 3 (1-indexed)", pos)
	}
	scanOne(t, db, &pos, `select instr('abc', 'z')`)
	if pos != 0 {
		t.Errorf("instr (no match)=%d, want 0", pos)
	}
}

func TestStr_HexUnhex(t *testing.T) {
	db := openDB(t)
	var hex string
	scanOne(t, db, &hex, `select hex('hi')`)
	if hex != "6869" {
		t.Errorf("hex=%q, want '6869'", hex)
	}
	// unhex was introduced in SQLite 3.41; gate on version.
	if v := sqliteVersion(t, db); v < "3.41" {
		t.Skipf("unhex requires SQLite >= 3.41, have %s", v)
	}
	var bs []byte
	scanOne(t, db, &bs, `select unhex('6869')`)
	if string(bs) != "hi" {
		t.Errorf("unhex=%q, want 'hi'", string(bs))
	}
}

func TestStr_UpperLower(t *testing.T) {
	db := openDB(t)
	var up, lo string
	scanOne(t, db, &up, `select upper('Hello')`)
	scanOne(t, db, &lo, `select lower('Hello')`)
	if up != "HELLO" || lo != "hello" {
		t.Errorf("upper=%q lower=%q, want HELLO, hello", up, lo)
	}
}

func TestStr_TrimVariants(t *testing.T) {
	db := openDB(t)
	cases := []struct {
		sql, want string
	}{
		{`select trim('  hi  ')`, "hi"},
		{`select ltrim('  hi  ')`, "hi  "},
		{`select rtrim('  hi  ')`, "  hi"},
		{`select trim('xxyyhixxyy', 'xy')`, "hi"},
		{`select ltrim('xyhi', 'xy')`, "hi"},
		{`select rtrim('hixy', 'xy')`, "hi"},
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

func TestStr_LikeGlob(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `create table t (s text); insert into t values ('Apple'), ('apricot'), ('banana')`)

	var n int
	scanOne(t, db, &n, `select count(*) from t where s like 'a%'`)
	// LIKE is case-insensitive by default for ASCII.
	if n != 2 {
		t.Errorf("LIKE a%%: %d, want 2", n)
	}

	scanOne(t, db, &n, `select count(*) from t where s glob 'a*'`)
	// GLOB is case-sensitive.
	if n != 1 {
		t.Errorf("GLOB a*: %d, want 1", n)
	}

	// LIKE ESCAPE clause.
	scanOne(t, db, &n, `select 1 where '50%' like '50\%' escape '\'`)
	if n != 1 {
		t.Errorf("LIKE ESCAPE: %d, want 1", n)
	}
}

func TestStr_PrintfFormat(t *testing.T) {
	db := openDB(t)
	var got string
	// printf is the SQLite name; format is the SQL standard alias (3.38+).
	scanOne(t, db, &got, `select printf('%05d-%s', 7, 'go')`)
	if got != "00007-go" {
		t.Errorf("printf=%q, want '00007-go'", got)
	}
	if v := sqliteVersion(t, db); v >= "3.38" {
		var got2 string
		scanOne(t, db, &got2, `select format('%05d-%s', 7, 'go')`)
		if got2 != got {
			t.Errorf("format=%q, printf=%q (should match)", got2, got)
		}
	}
}

func TestStr_CharUnicode(t *testing.T) {
	db := openDB(t)
	var s string
	scanOne(t, db, &s, `select char(72, 105)`)
	if s != "Hi" {
		t.Errorf("char(72,105)=%q, want 'Hi'", s)
	}
	var cp int
	scanOne(t, db, &cp, `select unicode('A')`)
	if cp != 65 {
		t.Errorf("unicode('A')=%d, want 65", cp)
	}
}

func TestStr_Quote(t *testing.T) {
	db := openDB(t)
	var s string
	scanOne(t, db, &s, `select quote('it''s')`)
	// quote() doubles embedded single quotes and wraps in single quotes.
	if !strings.HasPrefix(s, "'") || !strings.HasSuffix(s, "'") {
		t.Errorf("quote=%q, want single-quote wrapped", s)
	}
}

func TestStr_Concat(t *testing.T) {
	db := openDB(t)
	var s string
	// The || operator is SQL standard string concatenation.
	scanOne(t, db, &s, `select 'hello' || ' ' || 'world'`)
	if s != "hello world" {
		t.Errorf("concat=%q, want 'hello world'", s)
	}
}

func TestStr_Coalesce(t *testing.T) {
	db := openDB(t)
	var s string
	scanOne(t, db, &s, `select coalesce(null, null, 'fallback')`)
	if s != "fallback" {
		t.Errorf("coalesce=%q, want 'fallback'", s)
	}
}
