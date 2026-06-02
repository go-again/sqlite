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

import "strings"

// NamedArg splits a `key=value` argument into its two halves and trims
// surrounding whitespace from each. If arg contains no '=', the whole
// string is returned as key with value="" — callers can then decide
// whether the arg is positional or malformed.
func NamedArg(arg string) (key, value string) {
	key, value, _ = strings.Cut(arg, "=")
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	return key, value
}

// Unquote strips one layer of SQLite-style quoting from val. The
// recognized delimiters are:
//
//   - '  (single quote, SQL string literal — escape `''`)
//   - "  (double quote, identifier — escape `""`)
//   - `  (backtick, MySQL-compatible identifier — escape `` `` )
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
