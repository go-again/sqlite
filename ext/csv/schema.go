package csv

import (
	"strconv"
	"strings"
	"unicode"
)

// buildSchema generates the `CREATE TABLE x(...)` DDL used when the
// caller didn't supply an explicit schema. Column names come from the
// header row (if header=on), else are c1, c2, c3, …. Every column is
// declared TEXT — without an explicit schema we don't know better and
// SQLite's affinity rules will coerce on the SELECT side.
func buildSchema(header bool, columns int, row []string) string {
	if 0 <= columns && columns < len(row) {
		row = row[:columns]
	}
	var b strings.Builder
	b.WriteString("CREATE TABLE x(")
	sep := ""
	for i, f := range row {
		b.WriteString(sep)
		// A NUL in a header cell would truncate the generated DDL at the
		// libc.CString boundary (a malformed CREATE TABLE); fall back to the
		// positional name instead, same as an empty cell.
		if header && f != "" && strings.IndexByte(f, 0) < 0 {
			b.WriteString(quoteIdent(f))
		} else {
			b.WriteByte('c')
			b.WriteString(strconv.Itoa(i + 1))
		}
		b.WriteString(" TEXT")
		sep = ","
	}
	for i := len(row); i < columns; i++ {
		b.WriteString(sep)
		b.WriteByte('c')
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(" TEXT")
		sep = ","
	}
	b.WriteByte(')')
	return b.String()
}

// affinity tracks the SQL type hint extracted from each column
// declaration in an explicit schema. Used in cursor.Column to choose
// between ResultInt64, ResultFloat, and ResultText.
type affinity int

const (
	affinityText affinity = iota
	affinityNumeric
	affinityInteger
	affinityReal
)

// parseAffinities scans a CREATE TABLE statement and assigns an
// affinity per column. Intentionally simpler than SQLite's full DDL
// parser: we find the opening `(`, split column declarations on `,`
// at paren-depth 1, and keyword-scan each one for INTEGER / INT /
// REAL / FLOAT / DOUBLE / NUMERIC. Anything else → TEXT affinity.
//
// Matches the SQLite-bundled `csv.c` behavior for the common case.
// Complex types (e.g. `VARCHAR(255)`) work because we look for the
// keyword anywhere in the declaration.
func parseAffinities(schema string) []affinity {
	open := strings.IndexByte(schema, '(')
	close := strings.LastIndexByte(schema, ')')
	if open < 0 || close < 0 || close < open {
		return nil
	}
	body := schema[open+1 : close]
	cols := splitColumns(body)
	out := make([]affinity, 0, len(cols))
	for _, decl := range cols {
		out = append(out, affinityOf(decl))
	}
	return out
}

func splitColumns(body string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(body[start:i]))
				start = i + 1
			}
		}
	}
	if rest := strings.TrimSpace(body[start:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

func affinityOf(decl string) affinity {
	// SQLite's rules: order matters — INT (and any string containing INT)
	// is integer affinity; TEXT / CHAR / CLOB → text; REAL / FLOA / DOUB
	// → real; everything else → numeric (we collapse numeric→integer for
	// the parse path, integer→real if the int parse fails).
	d := strings.ToUpper(decl)
	switch {
	case strings.Contains(d, "INT"):
		return affinityInteger
	case strings.Contains(d, "TEXT"), strings.Contains(d, "CHAR"), strings.Contains(d, "CLOB"):
		return affinityText
	case strings.Contains(d, "REAL"), strings.Contains(d, "FLOA"), strings.Contains(d, "DOUB"):
		return affinityReal
	case strings.Contains(d, "BLOB"), strings.TrimSpace(d) == "":
		return affinityText
	default:
		return affinityNumeric
	}
}

// quoteIdent wraps name in double quotes, doubling embedded quotes.
// Used to escape header-row column names that may contain spaces,
// punctuation, or SQL keywords.
func quoteIdent(name string) string {
	if isBareIdent(name) {
		return name
	}
	var b strings.Builder
	b.Grow(len(name) + 2)
	b.WriteByte('"')
	for _, r := range name {
		if r == '"' {
			b.WriteByte('"')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func isBareIdent(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if i == 0 && !(unicode.IsLetter(r) || r == '_') {
			return false
		}
		if i > 0 && !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return false
		}
	}
	return true
}
