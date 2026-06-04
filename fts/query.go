package fts

import (
	"strconv"
	"strings"
)

// Query is the interface implemented by every FTS5 MATCH expression builder
// in this package. Calling build() produces the string passed as the
// right-hand side of `MATCH ?` — escaping and grouping are handled here so
// callers don't have to think about FTS5's quoting rules.
//
// Raw lets you fall through to FTS5's native syntax when the builder doesn't
// cover something you need (e.g. column filters in legacy spelling).
type Query interface {
	Build() string
}

// Raw lets you pass an FTS5 MATCH expression verbatim. The string is bound
// as a parameter, so SQL injection is not a concern; FTS5-level injection is
// up to the caller.
func Raw(s string) Query { return rawQ(s) }

type rawQ string

func (r rawQ) Build() string { return string(r) }

// Term matches a single token. FTS5 special characters in s are escaped by
// double-quoting the whole term, which FTS5 treats as a string literal token.
// Empty s produces an empty quoted string; FTS5 will reject that at exec
// time — but library code never panics on user input. Pass the empty
// string through and let the eventual Search call surface FTS5's error.
func Term(s string) Query { return termQ(s) }

type termQ string

func (t termQ) Build() string {
	return ftsQuoteTerm(string(t))
}

// Phrase matches an exact ordered sequence of tokens (FTS5 "phrase").
//
// All tokens are joined inside a single pair of double-quotes so FTS5 treats
// them as one phrase — adjacency required. (Separate quoted tokens would be
// implicitly AND-ed instead, which is what Term + Term gives you.)
// Zero tokens produces an empty quoted string; FTS5 rejects that at exec
// time, but the builder does not panic — caller may be threading user
// input through.
func Phrase(tokens ...string) Query { return phraseQ(tokens) }

type phraseQ []string

func (p phraseQ) Build() string {
	escaped := make([]string, len(p))
	for i, t := range p {
		// Inside a phrase, FTS5 still treats unbalanced double quotes as a
		// syntax error; double them to escape.
		escaped[i] = strings.ReplaceAll(t, `"`, `""`)
	}
	return `"` + strings.Join(escaped, " ") + `"`
}

// Prefix matches any token beginning with s. Equivalent to FTS5's `term*`
// syntax.
func Prefix(s string) Query { return prefixQ(s) }

type prefixQ string

func (p prefixQ) Build() string {
	// Prefix tokens cannot be quoted in FTS5, but they don't need special
	// escaping either — they are matched as token-character runs followed by
	// the literal star.
	return string(p) + "*"
}

// And combines sub-queries with FTS5's AND operator (default conjunction).
func And(qs ...Query) Query { return andQ(qs) }

type andQ []Query

func (a andQ) Build() string {
	parts := make([]string, len(a))
	for i, q := range a {
		parts[i] = "(" + q.Build() + ")"
	}
	return strings.Join(parts, " AND ")
}

// Or combines sub-queries with FTS5's OR operator.
func Or(qs ...Query) Query { return orQ(qs) }

type orQ []Query

func (o orQ) Build() string {
	parts := make([]string, len(o))
	for i, q := range o {
		parts[i] = "(" + q.Build() + ")"
	}
	return strings.Join(parts, " OR ")
}

// Not yields a NOT clause: positive AND NOT negatives. If you only have
// negatives, FTS5 returns no rows (matches no documents) — typically not what
// you want, so the API requires a positive.
func Not(positive Query, negatives ...Query) Query {
	return notQ{pos: positive, neg: negatives}
}

type notQ struct {
	pos Query
	neg []Query
}

func (n notQ) Build() string {
	if len(n.neg) == 0 {
		return n.pos.Build()
	}
	negs := make([]string, len(n.neg))
	for i, q := range n.neg {
		negs[i] = "(" + q.Build() + ")"
	}
	return "(" + n.pos.Build() + ") NOT (" + strings.Join(negs, " OR ") + ")"
}

// Near matches terms appearing within `distance` tokens of each other in any
// order. FTS5 default distance is 10 if you pass 0. Zero terms produces
// `NEAR()` which FTS5 rejects at exec time; the builder does not panic.
func Near(distance int, terms ...string) Query {
	return nearQ{dist: distance, terms: terms}
}

type nearQ struct {
	dist  int
	terms []string
}

func (n nearQ) Build() string {
	quoted := make([]string, len(n.terms))
	for i, t := range n.terms {
		quoted[i] = ftsQuoteTerm(t)
	}
	if n.dist <= 0 {
		return "NEAR(" + strings.Join(quoted, " ") + ")"
	}
	return "NEAR(" + strings.Join(quoted, " ") + ", " + strconv.Itoa(n.dist) + ")"
}

// Column scopes a sub-query to a specific column of the FTS5 table.
// Equivalent to FTS5's `{column}: query` syntax.
//
// Note: the column name is interpolated unquoted into the FTS5 match
// expression — FTS5's column-filter syntax doesn't accept quoted
// identifiers. Callers passing names with FTS5 metacharacters (spaces,
// colons, operators) will produce a malformed query that FTS5 rejects
// at exec time. Use [ValidIdent] at the API boundary if you accept
// column names from untrusted input.
func Column(name string, q Query) Query {
	return columnQ{col: name, q: q}
}

type columnQ struct {
	col string
	q   Query
}

func (c columnQ) Build() string {
	return c.col + ": (" + c.q.Build() + ")"
}

// ftsQuoteTerm wraps a term in double-quotes for FTS5, doubling any embedded
// double-quote characters. This lets us pass arbitrary user input without
// worrying about FTS5 operator keywords getting interpreted.
func ftsQuoteTerm(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
