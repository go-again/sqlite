package sqlid

import "testing"

func TestNamedArg(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantKey string
		wantVal string
	}{
		{"k=v", "k", "v"},
		{"  k  =  v  ", "k", "v"},
		{"key=", "key", ""},
		{"=value", "", "value"},
		{"bare", "bare", ""},
		{"", "", ""},
		{"k=a=b", "k", "a=b"}, // only the first '=' is the separator
	} {
		gotKey, gotVal := NamedArg(tc.in)
		if gotKey != tc.wantKey || gotVal != tc.wantVal {
			t.Errorf("NamedArg(%q) = (%q, %q), want (%q, %q)",
				tc.in, gotKey, gotVal, tc.wantKey, tc.wantVal)
		}
	}
}

func TestUnquote(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		// Single quotes — SQL string literal.
		{`'hello'`, "hello"},
		{`'it''s'`, "it's"},
		{`''`, ""},
		// Double quotes — identifier.
		{`"col"`, "col"},
		{`"a""b"`, `a"b`},
		// Backticks — MySQL-compat identifier.
		{"`col`", "col"},
		{"`a``b`", "a`b"},
		// Square brackets — MSSQL-compat identifier; no escape inside.
		{`[col]`, "col"},
		{`[with space]`, "with space"},
		// Unquoted / mismatched — returned unchanged.
		{"bare", "bare"},
		{"'mismatched\"", "'mismatched\""},
		{"", ""},
		{"x", "x"},
		// One-character strings cannot be quoted.
		{"'", "'"},
	} {
		got := Unquote(tc.in)
		if got != tc.want {
			t.Errorf("Unquote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
