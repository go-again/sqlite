// Package regexpgorm provides gorm helpers around the [ext/regexp]
// extension. The headline feature is [WhereRegex], which combines a
// REGEXP filter with a GLOB prefix so SQLite's planner can do a range
// scan on the GLOB clause and treat the regex as a residual filter on
// the survivors.
//
// # Quick example
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    _ "github.com/go-again/sqlite/ext/regexp/auto"
//	    rxgorm "github.com/go-again/sqlite/ext/regexp/gorm"
//	)
//
//	var docs []Doc
//	db.Where(rxgorm.WhereRegex("title", `^Intro to .* with Go$`)).Find(&docs)
//
// The emitted SQL is:
//
//	title GLOB 'Intro to *' AND title REGEXP '^Intro to .* with Go$'
//
// SQLite rewrites `title GLOB 'Intro to *'` into a range scan
// (`title >= 'Intro to ' AND title < 'Intro to{'`); the REGEXP runs only
// on the narrower set of candidates the index returned.
//
// # When the helper degrades
//
// If the pattern is unanchored (no `^` at the front) or contains regex
// constructs at the front (alternation, character class), [WhereRegex]
// falls back to a plain `col REGEXP ?` clause — correctness preserved,
// no LIKE-optimization win available.
//
// # Requirements
//
// The `REGEXP` operator and `regexp_*` functions must be registered on
// the connection. The simplest wire-up is the blank-import
// auto-registration:
//
//	import _ "github.com/go-again/sqlite/ext/regexp/auto"
package regexpgorm

import (
	"gorm.io/gorm/clause"

	"github.com/go-again/sqlite/ext/regexp"
)

// WhereRegex returns a gorm [clause.Expression] that constrains column
// to match pattern using both a GLOB prefix hint (for SQLite's LIKE
// optimization) and the REGEXP operator (for the full pattern). Compose
// with [gorm.DB.Where] in any chain.
//
// Anchored patterns (`^literal-prefix…`) emit
// `column GLOB 'literal-prefix*' AND column REGEXP ?`. Unanchored
// patterns fall back to `column REGEXP ?`.
//
// The pattern is parsed once per call via [regexp.GlobPrefix]; callers
// constructing many similar clauses can cache the result themselves.
func WhereRegex(column, pattern string) clause.Expression {
	prefix := regexp.GlobPrefix(pattern)
	if prefix == "" || prefix == "*" {
		// Unanchored / unusable prefix — fall back to plain REGEXP.
		return clause.Expr{
			SQL:  column + " REGEXP ?",
			Vars: []any{pattern},
		}
	}
	return clause.Expr{
		SQL:  column + " GLOB ? AND " + column + " REGEXP ?",
		Vars: []any{prefix, pattern},
	}
}
