package sqlid

import (
	"database/sql/driver"
	"slices"
	"testing"
)

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
		// Bare (no '=') tokens — key="" so callers can detect via
		// `if key == ""`; value carries the trimmed bare token.
		{"bare", "", "bare"},
		{"  spaced  ", "", "spaced"},
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

func TestQuoteIdent(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"col", `"col"`},
		{"", `""`},
		{"with space", `"with space"`},
		{`em"bed`, `"em""bed"`},
		{`""`, `""""""`},
		{`"`, `""""`},
	} {
		got := QuoteIdent(tc.in)
		if got != tc.want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQuoteIdentBacktick(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"col", "`col`"},
		{"", "``"},
		{"with space", "`with space`"},
		{"em`bed", "`em``bed`"},
		{"``", "``````"},
	} {
		got := QuoteIdentBacktick(tc.in)
		if got != tc.want {
			t.Errorf("QuoteIdentBacktick(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidIdent(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", false},
		{"col", true},
		{"_under", true},
		{"Col1", true},
		{"a1_b2", true},
		{"1abc", false},
		{"with space", false},
		{"a-b", false},
		{"a.b", false},
		{"_", true},
		{"ñame", false}, // non-ASCII
		{`"col"`, false},
	} {
		got := ValidIdent(tc.in)
		if got != tc.want {
			t.Errorf("ValidIdent(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestToNamedValues(t *testing.T) {
	if got := ToNamedValues(nil); got != nil {
		t.Errorf("ToNamedValues(nil) = %v, want nil", got)
	}
	if got := ToNamedValues([]driver.Value{}); got != nil {
		t.Errorf("ToNamedValues(empty) = %v, want nil", got)
	}
	in := []driver.Value{42, "hello", []byte{1, 2}}
	want := []driver.NamedValue{
		{Ordinal: 1, Value: 42},
		{Ordinal: 2, Value: "hello"},
		{Ordinal: 3, Value: []byte{1, 2}},
	}
	got := ToNamedValues(in)
	if !slices.EqualFunc(got, want, func(a, b driver.NamedValue) bool {
		if a.Ordinal != b.Ordinal || a.Name != b.Name {
			return false
		}
		switch av := a.Value.(type) {
		case []byte:
			bv, ok := b.Value.([]byte)
			return ok && slices.Equal(av, bv)
		default:
			return a.Value == b.Value
		}
	}) {
		t.Errorf("ToNamedValues mismatch:\ngot  %#v\nwant %#v", got, want)
	}
}
