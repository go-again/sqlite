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
// share one parser, plus the driver.Value coercion helpers (AsString /
// AsInt64 / AsFloat) the vtab cursors use to read row values back from
// child queries. It is intentionally minimal — anything beyond these
// helpers belongs in the calling extension.
package sqlid

import (
	"database/sql/driver"
	"fmt"
	"strconv"
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

// QuoteString renders s as a single-quoted SQL string literal, doubling any
// embedded single quotes — the form a vtab argument-string parser unquotes
// (e.g. csv(data='…') / lines(filename='…')). Consolidates the byte-identical
// sqlString helpers that ext/csv and ext/lines each carried.
func QuoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
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

// AsString coerces a driver.Value to its textual form the way the
// vtab ext/* cursors expect when reading a value back from a child
// query (e.g. a pivot column-key's display name, a bloom membership
// key). NULL becomes the empty string; string / []byte pass through
// (the latter is TEXT-as-bytes); int64 and float64 use the shortest
// round-trippable rendering; bool renders "true"/"false". Anything
// else falls back to fmt.Sprint.
//
// The numeric renderings are chosen to be byte-identical to the
// fmt-based formatters the ext/* copies used before consolidation:
// strconv.FormatInt(x, 10) == fmt.Sprintf("%d", x), and
// strconv.FormatFloat(x, 'g', -1, 64) == fmt.Sprintf("%g", x).
func AsString(v driver.Value) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return fmt.Sprint(v)
}

// AsInt64 coerces a driver.Value to int64. int64 passes through;
// float64 truncates toward zero; string / []byte are parsed as a
// base-10 integer (full-string match — a non-numeric or partially
// numeric value yields 0). Anything else, including NULL, yields 0.
//
// The string/[]byte branch uses strconv.ParseInt, which requires the
// whole token to be a valid integer; callers that need fmt.Sscan's
// leading-prefix tolerance must keep their own helper (see
// ext/spellfix1, which is intentionally not routed here).
func AsInt64(v driver.Value) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case []byte:
		n, _ := strconv.ParseInt(string(x), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

// AsFloat coerces a driver.Value to float64. float64 passes through;
// int64 widens; anything else, including string / []byte and NULL,
// yields 0. (SQLite hands numeric columns back as int64/float64, so
// the textual branches the integer path needs don't arise here.)
func AsFloat(v driver.Value) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	}
	return 0
}

// IsAlreadyExistsErr reports whether err carries SQLite's "table X
// already exists" signal. SQLite returns SQLITE_ERROR (no extended
// code) for this; we string-match the engine's stable message
// fragment. Lowercased for safety against future-version case changes.
//
// Used by vec.Create and fts.New to map the raw SQLite error to their
// typed ErrAlreadyExists sentinels.
func IsAlreadyExistsErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}
