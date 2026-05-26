package fts

import (
	"fmt"
	"strings"
)

// Tokenizer is the interface every FTS5 tokenizer configuration implements.
// encode returns the value of the FTS5 `tokenize=` option as it should appear
// in CREATE VIRTUAL TABLE, including any single-quote escaping required by
// the FTS5 grammar.
type Tokenizer interface {
	encode() string
}

// Unicode61 is FTS5's default tokenizer — Unicode-aware, case-folding, and
// configurable. See https://www.sqlite.org/fts5.html section 4.3.4.
type Unicode61 struct {
	// RemoveDiacritics: 0 = preserve, 1 = strip ASCII-compatible diacritics
	// (default in FTS5), 2 = strip all. Set Set to set; zero means use FTS5's
	// default.
	RemoveDiacritics int

	// Categories overrides which Unicode categories are considered token
	// characters. The empty string keeps FTS5's default ("L* N* Co").
	Categories string

	// Tokenchars adds a list of additional characters to treat as token
	// characters (e.g. "@.").
	Tokenchars string

	// Separators adds a list of additional characters to treat as
	// non-token characters.
	Separators string
}

func (u Unicode61) encode() string {
	var args []string
	args = append(args, "unicode61")
	if u.RemoveDiacritics > 0 {
		args = append(args, "remove_diacritics", fmt.Sprintf("%d", u.RemoveDiacritics))
	}
	if u.Categories != "" {
		args = append(args, "categories", quoteFTSArg(u.Categories))
	}
	if u.Tokenchars != "" {
		args = append(args, "tokenchars", quoteFTSArg(u.Tokenchars))
	}
	if u.Separators != "" {
		args = append(args, "separators", quoteFTSArg(u.Separators))
	}
	return "'" + strings.Join(args, " ") + "'"
}

// Ascii is FTS5's plain-ASCII tokenizer. Equivalent to unicode61 with
// remove_diacritics=2 and categories restricted to ASCII letters/digits,
// but cheaper. See https://www.sqlite.org/fts5.html section 4.3.4.
type Ascii struct {
	// Tokenchars adds characters to treat as token characters.
	Tokenchars string
	// Separators adds characters to treat as separators.
	Separators string
}

func (a Ascii) encode() string {
	var args []string
	args = append(args, "ascii")
	if a.Tokenchars != "" {
		args = append(args, "tokenchars", quoteFTSArg(a.Tokenchars))
	}
	if a.Separators != "" {
		args = append(args, "separators", quoteFTSArg(a.Separators))
	}
	return "'" + strings.Join(args, " ") + "'"
}

// Porter wraps a base tokenizer with Porter stemming so that "running",
// "runs", "ran" collapse to a common stem. Base typically holds a
// Unicode61 or Ascii config; nil Base means use Porter over FTS5's default
// (unicode61).
//
// See https://www.sqlite.org/fts5.html section 4.3.3.
type Porter struct {
	Base Tokenizer
}

func (p Porter) encode() string {
	if p.Base == nil {
		return "'porter'"
	}
	// FTS5 expects "porter <inner-tokenize-config>" with the inner config
	// inlined — i.e. without the outer quotes that the standalone form
	// produces. Strip them off.
	inner := p.Base.encode()
	inner = strings.TrimPrefix(strings.TrimSuffix(inner, "'"), "'")
	return "'porter " + inner + "'"
}

// Trigram is FTS5's trigram tokenizer (SQLite >= 3.34). Tokens are
// overlapping 3-character windows, which makes substring search and LIKE-
// style queries efficient.
//
// See https://www.sqlite.org/fts5.html section 4.3.5.
type Trigram struct {
	// CaseSensitive controls whether to fold case before tokenizing.
	CaseSensitive bool
}

func (tg Trigram) encode() string {
	if tg.CaseSensitive {
		return "'trigram case_sensitive 1'"
	}
	return "'trigram'"
}

// quoteFTSArg escapes a string for inclusion as a quoted argument inside an
// FTS5 tokenize= option. FTS5 wraps tokenizer args in single quotes; nested
// double-quotes are used to delimit per-arg values that contain spaces or
// punctuation. We always use double-quotes for nested values and escape any
// embedded double-quote by doubling it (SQL convention).
func quoteFTSArg(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
