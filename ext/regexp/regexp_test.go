package regexp_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/go-again/sqlite/ext/regexp"
	"github.com/go-again/sqlite/internal/testhelp"
)

func openDB(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, sc := testhelp.OpenPinned(t, "sqlite", ":memory:")
	testhelp.RegisterOn(t, sc, regexp.Register)
	return db, sc
}

func TestRegexp_Operator(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	cases := []struct {
		text, pat string
		want      bool
	}{
		{"hello", "^h", true},
		{"hello", "^H", false},
		{"abc123", `\d+`, true},
		{"abc", `\d+`, false},
		{"Hello World", `(?i)world`, true},
	}
	for _, tc := range cases {
		var got bool
		if err := sc.QueryRowContext(ctx, `SELECT ? REGEXP ?`, tc.text, tc.pat).Scan(&got); err != nil {
			t.Fatalf("%q REGEXP %q: %v", tc.text, tc.pat, err)
		}
		if got != tc.want {
			t.Errorf("%q REGEXP %q = %v, want %v", tc.text, tc.pat, got, tc.want)
		}
	}
}

func TestRegexp_Like(t *testing.T) {
	_, sc := openDB(t)
	var got bool
	if err := sc.QueryRowContext(context.Background(),
		`SELECT regexp_like('hello world', 'wo.ld')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("regexp_like should match")
	}
}

func TestRegexp_Count(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	cases := []struct {
		text, pat string
		start     any
		want      int64
	}{
		{"a b c d e", " ", nil, 4},
		{"aaaa", "a", nil, 4},
		{"abc 123 def 456", `\d+`, nil, 2},
		{"a b c d", " ", int64(4), 2}, // count from position 4 onward
	}
	for _, tc := range cases {
		var got int64
		var err error
		if tc.start == nil {
			err = sc.QueryRowContext(ctx, `SELECT regexp_count(?, ?)`, tc.text, tc.pat).Scan(&got)
		} else {
			err = sc.QueryRowContext(ctx, `SELECT regexp_count(?, ?, ?)`,
				tc.text, tc.pat, tc.start).Scan(&got)
		}
		if err != nil {
			t.Fatalf("(%q,%q): %v", tc.text, tc.pat, err)
		}
		if got != tc.want {
			t.Errorf("regexp_count(%q,%q)=%d, want %d", tc.text, tc.pat, got, tc.want)
		}
	}
}

func TestRegexp_Substr(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	cases := []struct {
		query string
		args  []any
		want  string
	}{
		{`SELECT regexp_substr(?, ?)`, []any{"hello world", `\w+`}, "hello"},
		{`SELECT regexp_substr(?, ?, 1, 2)`, []any{"a1 b2 c3", `\w\d`}, "b2"}, // 2nd match
		{`SELECT regexp_substr(?, ?, 1, 1, 1)`, []any{"foo=bar", `(\w+)=(\w+)`}, "foo"},
		{`SELECT regexp_substr(?, ?, 1, 1, 2)`, []any{"foo=bar", `(\w+)=(\w+)`}, "bar"},
	}
	for _, tc := range cases {
		var got string
		if err := sc.QueryRowContext(ctx, tc.query, tc.args...).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		if got != tc.want {
			t.Errorf("%s -> %q, want %q", tc.query, got, tc.want)
		}
	}
}

func TestRegexp_Replace(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	cases := []struct {
		query string
		args  []any
		want  string
	}{
		{`SELECT regexp_replace(?, ?, ?)`,
			[]any{"hello world", "world", "Go"}, "hello Go"},
		{`SELECT regexp_replace(?, ?, ?)`,
			[]any{"a1b2c3", `\d`, "*"}, "a*b*c*"},
		{`SELECT regexp_replace(?, ?, ?, 1, 2)`, // 2nd match only
			[]any{"a1b2c3", `\d`, "*"}, "a1b*c3"},
		{`SELECT regexp_replace(?, ?, ?)`,
			[]any{"foo=bar", `(\w+)=(\w+)`, "$2=$1"}, "bar=foo"},
	}
	for _, tc := range cases {
		var got string
		if err := sc.QueryRowContext(ctx, tc.query, tc.args...).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		if got != tc.want {
			t.Errorf("%s -> %q, want %q", tc.query, got, tc.want)
		}
	}
}

func TestRegexp_Instr(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	// 1-based byte position of the first \d+ match in "abc 123 def".
	var got int64
	if err := sc.QueryRowContext(ctx,
		`SELECT regexp_instr('abc 123 def', '\d+')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 5 {
		t.Errorf("regexp_instr=%d, want 5", got)
	}

	// endoption=1 returns the byte position just past the match.
	if err := sc.QueryRowContext(ctx,
		`SELECT regexp_instr('abc 123 def', '\d+', 1, 1, 1)`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 8 {
		t.Errorf("regexp_instr(endoption=1)=%d, want 8", got)
	}

	// 0 when no match.
	if err := sc.QueryRowContext(ctx,
		`SELECT regexp_instr('abcdef', '\d+')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("regexp_instr no-match=%d, want 0", got)
	}
}

func TestRegexp_BadPattern(t *testing.T) {
	_, sc := openDB(t)
	_, err := sc.ExecContext(context.Background(),
		`SELECT regexp_like('hello', '(unclosed')`)
	if err == nil || !strings.Contains(err.Error(), "regexp:") {
		t.Errorf("got %v, want regexp: error", err)
	}
}

func TestRegexp_Unicode(t *testing.T) {
	// Go's regexp is Unicode-aware by default. Verify a multibyte match.
	_, sc := openDB(t)
	var got bool
	if err := sc.QueryRowContext(context.Background(),
		`SELECT 'café' REGEXP 'caf[éè]'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("Unicode REGEXP failed")
	}
}
