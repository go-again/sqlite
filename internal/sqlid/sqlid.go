// Package sqlid implements small SQLite-flavored parsing helpers shared
// across loadable extensions (ext/closure, ext/pivot, ext/statement, …).
//
// SQLite vtab CREATE arguments arrive as raw token slices. Each token is
// either a positional value or a named arg (key=value). Quoting follows
// SQLite's keyword rules: ' " [] backticks all serve as identifier or
// string delimiters, and the doubled-quote escape applies inside same-kind
// quoting.
//
// This package centralizes NamedArg + Unquote so the various ext/* ports
// share one parser. It is intentionally minimal — anything beyond the two
// helpers belongs in the calling extension.
package sqlid

import (
	"database/sql/driver"
	"strings"
)

// QuoteIdent renders an arbitrary string as a SQL identifier using
// SQLite's canonical double-quote escape: wrap in `"…"` and double
// any embedded double-quotes. This is what the doc-comments in the
// ext/* sub-packages mean by "quote identifier" — every loadable
// extension that builds SQL strings against shadow tables had its own
// byte-identical copy until this helper consolidated them.
func QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// QuoteIdentBacktick renders s as a backtick-quoted identifier
// (`…`), doubling any embedded backticks. SQLite accepts this form
// alongside the canonical double-quoted shape; sqlite-vec's vec0
// CREATE VIRTUAL TABLE constructor and FTS5's fts5 constructor both
// expect bare identifiers, so vec / fts pass arguments through this
// helper outside the constructor and rely on ValidIdent inside it.
func QuoteIdentBacktick(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// ValidIdent reports whether s is a safe SQL identifier — the
// conservative ASCII subset: leading letter or underscore, then
// letters, digits, or underscores. Used to guard against SQL
// injection at the API boundary when callers pass arbitrary strings
// as table or column names that have to be interpolated unquoted
// (e.g. inside the vec0 / FTS5 CREATE VIRTUAL TABLE constructors,
// which don't accept quoted identifiers).
func ValidIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// ToNamedValues converts a positional []driver.Value to the
// []driver.NamedValue shape that the non-deprecated `Stmt.QueryContext`
// / `Conn.ExecContext` paths expect. Ordinals are 1-based. Used by
// the vtab-side ext/* packages that hand bind arguments through the
// driver from inside `Filter` / `Update` callbacks.
func ToNamedValues(args []driver.Value) []driver.NamedValue {
	if len(args) == 0 {
		return nil
	}
	out := make([]driver.NamedValue, len(args))
	for i, v := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}

// NamedArg splits a `key=value` argument into its two halves and trims
// surrounding whitespace from each. If arg contains no '=', the whole
// string is returned as the value with key="" — callers reading the
// result as `if key == ""` then correctly detect "positional or
// malformed arg" rather than seeing a bare token slip through as a
// (key, "") pair with empty value.
func NamedArg(arg string) (key, value string) {
	k, v, ok := strings.Cut(arg, "=")
	if !ok {
		return "", strings.TrimSpace(k)
	}
	return strings.TrimSpace(k), strings.TrimSpace(v)
}

// Unquote strips one layer of SQLite-style quoting from val. The
// recognized delimiters are:
//
//   - '  (single quote, SQL string literal — escape `”`)
//   - "  (double quote, identifier — escape `""`)
//   - `  (backtick, MySQL-compatible identifier — escape “ “ )
//   - [] (square brackets, MSSQL-compatible identifier — no escape inside)
//
// If val does not begin and end with a matching delimiter, it is
// returned unchanged. Strings shorter than two characters cannot be
// quoted and are also returned unchanged.
//
// Reference: https://sqlite.org/lang_keywords.html
func Unquote(val string) string {
	if len(val) < 2 {
		return val
	}
	first, last := val[0], val[len(val)-1]
	inner := val[1 : len(val)-1]
	if first == '[' && last == ']' {
		return inner
	}
	if first != last {
		return val
	}
	var old, new string
	switch first {
	default:
		return val
	case '`':
		old, new = "``", "`"
	case '"':
		old, new = `""`, `"`
	case '\'':
		old, new = `''`, `'`
	}
	return strings.ReplaceAll(inner, old, new)
}
