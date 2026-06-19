// Package text adds Unicode-aware string scalar functions that SQLite's
// built-ins and ext/unicode don't cover:
//
//	text_reverse(s)          -- reverse by rune ('abç' → 'çba')
//	text_repeat(s, n)        -- s concatenated n times
//	text_lpad(s, n[, pad])   -- left-pad to n runes with pad (default ' ')
//	text_rpad(s, n[, pad])   -- right-pad to n runes
//	text_split(s, sep, n)    -- the 1-based nth field of s split on sep
//
// All counts are in runes, not bytes. Casing/normalization live in
// ext/unicode; digests in ext/hash; codecs in ext/encode.
package text

import (
	"errors"
	"strings"
	"unicode/utf8"

	sqlite "gosqlite.org"
)

// maxResult caps the size of text_repeat / text_*pad output to keep a hostile
// count argument from exhausting memory.
const maxResult = 1 << 28 // 256 MiB

// Register installs the text_* scalar functions on c.
//
// Per-connection registration. For pool-wide install, blank-import the auto
// sub-package:
//
//	import _ "gosqlite.org/ext/text/auto"
func Register(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc("text_reverse", textReverse, true),
		c.RegisterFunc("text_repeat", textRepeat, true),
		c.RegisterFunc("text_lpad", textLpad, true),
		c.RegisterFunc("text_rpad", textRpad, true),
		c.RegisterFunc("text_split", textSplit, true),
	)
}

func textReverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func textRepeat(s string, n int64) (string, error) {
	if n <= 0 || s == "" {
		return "", nil
	}
	// Overflow-safe: s is non-empty here, so len(s) >= 1; division avoids the
	// int64 wrap that len(s)*n would suffer for a hostile n.
	if n > maxResult/int64(len(s)) {
		return "", errors.New("text_repeat: result too large")
	}
	return strings.Repeat(s, int(n)), nil
}

func textLpad(s string, n int64, pad ...string) (string, error) { return pad2(s, n, true, pad) }
func textRpad(s string, n int64, pad ...string) (string, error) { return pad2(s, n, false, pad) }

func pad2(s string, n int64, left bool, pad []string) (string, error) {
	p := " "
	if len(pad) > 0 && pad[0] != "" {
		p = pad[0]
	}
	cur := int64(utf8.RuneCountInString(s))
	if n <= cur {
		return s, nil
	}
	need := n - cur
	// Overflow-safe: p is non-empty here, so len(p) >= 1; the division avoids
	// the int64 wrap that need*len(p) would suffer for a hostile n (which would
	// otherwise pass the guard and drive an unbounded allocation loop).
	if need > (maxResult-int64(len(s)))/int64(len(p)) {
		return "", errors.New("text_pad: result too large")
	}
	padRunes := []rune(p)
	var b strings.Builder
	for i := range need {
		b.WriteRune(padRunes[i%int64(len(padRunes))])
	}
	if left {
		return b.String() + s, nil
	}
	return s + b.String(), nil
}

func textSplit(s, sep string, n int64) string {
	if sep == "" {
		if n == 1 {
			return s
		}
		return ""
	}
	parts := strings.Split(s, sep)
	idx := n - 1 // 1-based
	if idx < 0 || idx >= int64(len(parts)) {
		return ""
	}
	return parts[idx]
}
