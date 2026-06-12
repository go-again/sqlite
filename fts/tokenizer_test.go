package fts

import "testing"

// wellFormedSQLLiteral reports whether s is a single SQL single-quoted string
// literal: it begins and ends with ', and every interior ' is doubled (no lone
// ' that would terminate the literal early). This is the property the
// tokenizer-arg breakout (sweep #3 F1) violated.
func wellFormedSQLLiteral(s string) bool {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return false
	}
	body := s[1 : len(s)-1]
	for i := 0; i < len(body); {
		if body[i] != '\'' {
			i++
			continue
		}
		j := i
		for j < len(body) && body[j] == '\'' {
			j++
		}
		if (j-i)%2 != 0 { // a lone (un-doubled) quote breaks out
			return false
		}
		i = j
	}
	return true
}

func TestTokenizer_SingleQuoteNoBreakout(t *testing.T) {
	// A single quote in a free-form tokenizer field must not terminate the
	// outer FTS5 string literal early. Before F1, encode() escaped only the
	// inner double-quote, so each of these produced a malformed literal.
	cases := map[string]Tokenizer{
		"unicode61 tokenchars": Unicode61{Tokenchars: "a'b"},
		"unicode61 separators": Unicode61{Separators: "x'"},
		"unicode61 categories": Unicode61{Categories: "L*'"},
		"ascii tokenchars":     Ascii{Tokenchars: "'"},
		"ascii separators":     Ascii{Separators: "p'q'r"},
		"porter over unicode":  Porter{Base: Unicode61{Tokenchars: "a'b"}},
		"porter over ascii":    Porter{Base: Ascii{Separators: "x'y'"}},
	}
	for name, tk := range cases {
		got := tk.encode()
		if !wellFormedSQLLiteral(got) {
			t.Errorf("%s: encode produced a malformed SQL literal (breakout): %s", name, got)
		}
	}
}

func TestValidateTokenizer_RejectsNUL(t *testing.T) {
	if err := validateTokenizer(Unicode61{Tokenchars: "a\x00b"}); err == nil {
		t.Error("validateTokenizer should reject a NUL in tokenchars (it truncates the C string)")
	}
	if err := validateTokenizer(Porter{Base: Ascii{Separators: "\x00"}}); err == nil {
		t.Error("validateTokenizer should recurse into Porter.Base and reject NUL")
	}
	// A clean value and a value with a legitimate control byte (tab separator)
	// must pass — only NUL is refused.
	if err := validateTokenizer(Unicode61{Tokenchars: "@."}); err != nil {
		t.Errorf("validateTokenizer rejected a clean value: %v", err)
	}
	if err := validateTokenizer(Ascii{Separators: "\t"}); err != nil {
		t.Errorf("validateTokenizer rejected a tab separator: %v", err)
	}
}

// FuzzTokenizerEncode asserts that no caller-supplied tokenchars value, however
// hostile, can make encode() emit a malformed (breakout) SQL literal.
func FuzzTokenizerEncode(f *testing.F) {
	for _, s := range []string{`@.`, `a'b`, `''`, `"`, `'"'`, "a\x00b", `\`, `'; DROP TABLE t; --`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !wellFormedSQLLiteral(Unicode61{Tokenchars: s}.encode()) {
			t.Fatalf("unicode61 tokenchars %q encoded to a malformed literal", s)
		}
		if !wellFormedSQLLiteral(Ascii{Separators: s}.encode()) {
			t.Fatalf("ascii separators %q encoded to a malformed literal", s)
		}
		if !wellFormedSQLLiteral(Porter{Base: Unicode61{Tokenchars: s}}.encode()) {
			t.Fatalf("porter/unicode61 tokenchars %q encoded to a malformed literal", s)
		}
	})
}
