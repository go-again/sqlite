package sqlid

import (
	"database/sql/driver"
	"testing"
)

func BenchmarkQuoteIdent_Simple(b *testing.B) {
	for b.Loop() {
		_ = QuoteIdent("users")
	}
}

func BenchmarkQuoteIdent_WithEmbeddedQuote(b *testing.B) {
	for b.Loop() {
		_ = QuoteIdent(`em"bed`)
	}
}

func BenchmarkQuoteIdentBacktick_Simple(b *testing.B) {
	for b.Loop() {
		_ = QuoteIdentBacktick("users")
	}
}

func BenchmarkValidIdent(b *testing.B) {
	cases := []string{"a", "abc", "snake_case_name", "MixedCase123", "_underscore"}
	for b.Loop() {
		for _, s := range cases {
			_ = ValidIdent(s)
		}
	}
}

func BenchmarkValidIdent_Reject(b *testing.B) {
	for b.Loop() {
		_ = ValidIdent("1starts-with-digit")
	}
}

func BenchmarkToNamedValues_4(b *testing.B) {
	args := []driver.Value{1, "two", 3.0, []byte{4}}
	for b.Loop() {
		_ = ToNamedValues(args)
	}
}

func BenchmarkNamedArg(b *testing.B) {
	for b.Loop() {
		_, _ = NamedArg("tablename=users")
	}
}

func BenchmarkUnquote_NoQuote(b *testing.B) {
	for b.Loop() {
		_ = Unquote("bare")
	}
}

func BenchmarkUnquote_DoubleQuote(b *testing.B) {
	for b.Loop() {
		_ = Unquote(`"col"`)
	}
}
