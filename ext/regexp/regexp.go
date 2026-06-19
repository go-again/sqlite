// Package regexp provides Go-regexp-syntax SQL functions plus the
// binary `REGEXP` operator SQLite leaves unimplemented by default.
//
// # Functions
//
//   - regexp(pattern, text) → BOOL: REGEXP operator backing.
//   - regexp_like(text, pattern) → BOOL.
//   - regexp_count(text, pattern [, start]) → INTEGER.
//   - regexp_instr(text, pattern [, start [, N [, endoption [, subexpr]]]]) → INTEGER.
//   - regexp_substr(text, pattern [, start [, N [, subexpr]]]) → TEXT.
//   - regexp_replace(text, pattern, replacement [, start [, N]]) → TEXT.
//
// Implementation uses the standard [regexp] package. Patterns follow
// [RE2 syntax]; PCRE-only features (backreferences, lookahead, etc.) are
// not supported and surface as a "regexp: parse pattern: …" error.
//
// Position arguments (start, N, subexpr) are 1-based to match the
// nalgeon/sqlean convention. start defaults to 1, N defaults to 1
// (first match), subexpr defaults to 0 (whole match).
//
// Ported from [ncruces/ext/regexp] with the same function lineup and
// semantics.
//
// # Usage
//
//	import (
//	    sqlite "gosqlite.org"
//	    "gosqlite.org/ext/regexp"
//	)
//
//	if err := regexp.Register(conn); err != nil { ... }
//
//	rows, _ := db.Query(`SELECT name FROM users WHERE name REGEXP ?`, `^[A-Z]`)
//
// For pool-wide install via [gosqlite.org.Driver.ConnectHook],
// blank-import the auto sub-package:
//
//	import _ "gosqlite.org/ext/regexp/auto"
//
// [RE2 syntax]: https://github.com/google/re2/wiki/Syntax
// [ncruces/ext/regexp]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/regexp
package regexp

import (
	"errors"
	"fmt"
	gore "regexp"
	goresyntax "regexp/syntax"
	"strings"
	"unicode/utf8"

	sqlite "gosqlite.org"
)

// Exported names of the SQL functions Register installs. Exposed as
// constants so callers can build queries that reference them without
// re-hardcoding the string. `FuncRegexp` is also the REGEXP operator.
const (
	FuncRegexp        = "regexp"
	FuncRegexpLike    = "regexp_like"
	FuncRegexpCount   = "regexp_count"
	FuncRegexpInstr   = "regexp_instr"
	FuncRegexpSubstr  = "regexp_substr"
	FuncRegexpReplace = "regexp_replace"
)

// Register installs the REGEXP operator plus the regexp_* SQL function
// lineup on c.
func Register(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc(FuncRegexp, regexpMatch, true),
		c.RegisterFunc(FuncRegexpLike, regexpLike, true),
		c.RegisterFunc(FuncRegexpCount, regexpCount, true),
		c.RegisterFunc(FuncRegexpInstr, regexpInstr, true),
		c.RegisterFunc(FuncRegexpSubstr, regexpSubstr, true),
		c.RegisterFunc(FuncRegexpReplace, regexpReplace, true),
	)
}

// GlobPrefix returns a GLOB pattern that matches strings whose start
// equals the anchored literal prefix of expr. Useful for the documented
// LIKE-optimization win:
//
//	WHERE col GLOB ? AND col REGEXP ?
//
// SQLite's planner can rewrite `col GLOB 'prefix*'` into a range scan
// (`col >= 'prefix' AND col < 'prefiy'`); the REGEXP clause filters the
// candidates. Returns `*` (match anything) if expr is unanchored, and
// `""` if expr can't possibly match anything (invalid regex, etc.).
//
// Implementation walks the compiled regexp program from
// [regexp/syntax], collecting literal runes until it hits a non-literal
// instruction or a multi-rune class. Matches ncruces upstream
// semantics.
func GlobPrefix(expr string) string {
	re, err := goresyntax.Parse(expr, goresyntax.Perl)
	if err != nil {
		return "" // no match possible
	}
	prog, err := goresyntax.Compile(re.Simplify())
	if err != nil {
		return ""
	}
	i := &prog.Inst[prog.Start]
	var empty goresyntax.EmptyOp
loopHeader:
	for {
		switch i.Op {
		case goresyntax.InstFail:
			return ""
		case goresyntax.InstCapture, goresyntax.InstNop:
			// skip
		case goresyntax.InstEmptyWidth:
			empty |= goresyntax.EmptyOp(i.Arg)
		default:
			break loopHeader
		}
		i = &prog.Inst[i.Out]
	}
	if empty&goresyntax.EmptyBeginText == 0 {
		return "*"
	}
	var glob strings.Builder
loopBody:
	for {
		switch i.Op {
		case goresyntax.InstFail:
			return ""
		case goresyntax.InstCapture, goresyntax.InstEmptyWidth, goresyntax.InstNop:
			// skip
		case goresyntax.InstRune, goresyntax.InstRune1:
			if len(i.Rune) != 1 || goresyntax.Flags(i.Arg)&goresyntax.FoldCase != 0 {
				break loopBody
			}
			switch r := i.Rune[0]; r {
			case '*', '?', '[', utf8.RuneError:
				break loopBody
			default:
				glob.WriteRune(r)
			}
		default:
			break loopBody
		}
		i = &prog.Inst[i.Out]
	}
	glob.WriteByte('*')
	return glob.String()
}

// REGEXP operator semantics: `text REGEXP pattern` calls regexp(pattern, text).
// SQLite invokes the function with args in (pattern, text) order.
func regexpMatch(pattern, text string) (bool, error) {
	re, err := compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(text), nil
}

func regexpLike(text, pattern string) (bool, error) {
	re, err := compile(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(text), nil
}

func regexpCount(text, pattern string, startOpt ...int64) (int64, error) {
	re, err := compile(pattern)
	if err != nil {
		return 0, err
	}
	start := int64(1)
	if len(startOpt) > 0 {
		start = startOpt[0]
	}
	t := []byte(text)
	pos := byteOffset(t, start)
	if pos > len(t) {
		return 0, nil
	}
	return int64(len(re.FindAll(t[pos:], -1))), nil
}

// regexpInstr returns the 1-based byte position of the N-th match (or its
// end, if endoption is non-zero) of pattern in text starting at start.
// subexpr=0 selects the whole match; >0 selects a capture group.
// Returns 0 when no match is found, matching nalgeon/sqlean.
func regexpInstr(text, pattern string, opts ...int64) (int64, error) {
	re, err := compile(pattern)
	if err != nil {
		return 0, err
	}
	start, n, endOption, subexpr := defaults(opts, 1, 1, 0, 0)
	loc, err := nthMatch(re, []byte(text), start, n, int(subexpr))
	if err != nil {
		return 0, err
	}
	if loc == nil {
		return 0, nil
	}
	if endOption != 0 {
		return int64(loc[1] + 1), nil
	}
	return int64(loc[0] + 1), nil
}

func regexpSubstr(text, pattern string, opts ...int64) (string, error) {
	re, err := compile(pattern)
	if err != nil {
		return "", err
	}
	start, n, subexpr, _ := defaults(opts, 1, 1, 0, 0)
	loc, err := nthMatch(re, []byte(text), start, n, int(subexpr))
	if err != nil {
		return "", err
	}
	if loc == nil {
		return "", nil
	}
	return text[loc[0]:loc[1]], nil
}

func regexpReplace(text, pattern, replacement string, opts ...int64) (string, error) {
	re, err := compile(pattern)
	if err != nil {
		return "", err
	}
	start, n, _, _ := defaults(opts, 1, 0, 0, 0)
	t := []byte(text)
	repl := []byte(replacement)
	pos := byteOffset(t, start)
	if pos > len(t) {
		return text, nil
	}

	if n > 0 {
		// Replace the N-th match only.
		all := re.FindAllSubmatchIndex(t[pos:], int(n))
		if int64(len(all)) < n {
			return text, nil
		}
		loc := all[n-1]
		out := make([]byte, 0, len(t)+len(repl))
		out = append(out, t[:pos+loc[0]]...)
		out = re.Expand(out, repl, t[pos:], loc)
		out = append(out, t[pos+loc[1]:]...)
		return string(out), nil
	}
	// Replace every match from pos onward.
	out := make([]byte, 0, len(t)+len(repl))
	out = append(out, t[:pos]...)
	out = append(out, re.ReplaceAll(t[pos:], repl)...)
	return string(out), nil
}

// compile is a thin wrapper around regexp.Compile that prefixes errors
// with "regexp: " so they're identifiable at the SQL layer.
func compile(pattern string) (*gore.Regexp, error) {
	re, err := gore.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("regexp: parse pattern: %w", err)
	}
	return re, nil
}

// defaults pulls up to 4 int64 options out of opts, falling back to the
// provided defaults for missing entries. opts ordering matches the
// function-specific argument order documented above.
func defaults(opts []int64, d0, d1, d2, d3 int64) (a, b, c, d int64) {
	a, b, c, d = d0, d1, d2, d3
	if len(opts) > 0 {
		a = opts[0]
	}
	if len(opts) > 1 {
		b = opts[1]
	}
	if len(opts) > 2 {
		c = opts[2]
	}
	if len(opts) > 3 {
		d = opts[3]
	}
	return
}

// byteOffset converts a 1-based rune position into a byte offset into t.
// Out-of-range start clamps to the end of the string.
func byteOffset(t []byte, start int64) int {
	if start <= 1 {
		return 0
	}
	skip := start - 1
	for pos := range string(t) {
		if skip == 0 {
			return pos
		}
		skip--
	}
	return len(t)
}

// nthMatch finds the N-th match of re in t starting from byte offset
// derived from start. subexpr=0 returns the full match; subexpr>0 returns
// the corresponding capture group. Returns nil if no match. Errors only
// on patently bad inputs (negative n).
func nthMatch(re *gore.Regexp, t []byte, start, n int64, subexpr int) ([]int, error) {
	if n < 1 {
		n = 1
	}
	pos := byteOffset(t, start)
	if pos > len(t) {
		return nil, nil
	}
	region := t[pos:]
	var loc []int
	if subexpr == 0 {
		all := re.FindAllIndex(region, int(n))
		if int64(len(all)) < n {
			return nil, nil
		}
		loc = all[n-1]
	} else {
		all := re.FindAllSubmatchIndex(region, int(n))
		if int64(len(all)) < n {
			return nil, nil
		}
		hit := all[n-1]
		// subexpr indexing: hit[2*subexpr], hit[2*subexpr+1].
		if 2*subexpr+1 >= len(hit) {
			return nil, nil
		}
		loc = []int{hit[2*subexpr], hit[2*subexpr+1]}
	}
	loc[0] += pos
	loc[1] += pos
	return loc, nil
}
