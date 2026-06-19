package unicode

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	gouni "unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	sqlite "gosqlite.org"
)

// Option configures [Register]. Functional options keep optional behavior
// (like opting into the Unicode-aware LIKE override) discoverable in the
// godoc and extensible without breaking the API every time a new knob
// lands.
type Option func(*config)

type config struct {
	likeOverride bool
}

// WithLike opts the Unicode-aware LIKE override into the [Register] call.
//
// CAVEAT: overriding LIKE disables SQLite's LIKE optimization (the
// planner can no longer rewrite `col LIKE 'prefix%'` into a range scan).
// Off by default for that reason; opt in deliberately.
func WithLike() Option {
	return func(c *config) { c.likeOverride = true }
}

// Register installs the Unicode scalar functions on c, plus the two
// preset collations NOCASE_UNICODE and NOCASE_ACCENT.
//
// Pass [WithLike] to also install the Unicode-aware LIKE override. The
// override is off by default to preserve SQLite's LIKE optimization
// (see [WithLike] docs).
func Register(c *sqlite.Conn, opts ...Option) error {
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	var errs []error
	add := func(name string, fn any) {
		errs = append(errs, c.RegisterFunc(name, fn, true))
	}

	add("upper", upper)
	add("lower", lower)
	add("initcap", initcap)
	add("casefold", casefold)
	add("unaccent", unaccent)
	add("normalize", normalize)

	errs = append(errs,
		c.RegisterCollation("NOCASE_UNICODE", compareNoCase),
		c.RegisterCollation("NOCASE_ACCENT", compareNoCaseAccent),
	)

	if cfg.likeOverride {
		errs = append(errs, RegisterLikeOnly(c))
	}
	return errors.Join(errs...)
}

// RegisterLikeOnly installs the Unicode-aware LIKE override on c without
// touching the other functions. Use this when you want LIKE Unicode-aware
// but don't need the case mapping / normalization surface, or when you
// want to install LIKE on a different connection set than the other
// functions.
//
// CAVEAT: overriding LIKE disables SQLite's LIKE optimization. See
// [WithLike] for context.
func RegisterLikeOnly(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc("like", likeTwo, true),
		c.RegisterFunc("like", likeThree, true),
	)
}

// RegisterLocaleCollation installs a [collate.Collator]-backed collation
// named name on c, configured for the given BCP-47 language tag. Use this
// for language-aware ordering (Turkish dotless 'ı' adjacent to 'i',
// Swedish 'å' after 'z', etc.).
func RegisterLocaleCollation(c *sqlite.Conn, locale, name string) error {
	tag, err := language.Parse(locale)
	if err != nil {
		return fmt.Errorf("unicode: parse locale %q: %w", locale, err)
	}
	col := collate.New(tag)
	cmp := func(a, b string) int { return col.CompareString(a, b) }
	return c.RegisterCollation(name, cmp)
}

// --- scalar functions ---
//
// The first argument is typed `any` so SQL NULL passes through as Go
// nil and the function returns `(nil, nil)` — matching PostgreSQL
// semantics for these helpers. A typed-string first arg would coerce
// NULL into "" via the reflective register path, which is wrong.

func upper(text any, optLocale ...string) (any, error) {
	s, ok := nilOrString(text)
	if !ok {
		return nil, nil
	}
	if len(optLocale) == 0 {
		return strings.ToUpper(s), nil
	}
	tag, err := language.Parse(optLocale[0])
	if err != nil {
		return nil, fmt.Errorf("upper: bad locale: %w", err)
	}
	return cases.Upper(tag).String(s), nil
}

func lower(text any, optLocale ...string) (any, error) {
	s, ok := nilOrString(text)
	if !ok {
		return nil, nil
	}
	if len(optLocale) == 0 {
		return strings.ToLower(s), nil
	}
	tag, err := language.Parse(optLocale[0])
	if err != nil {
		return nil, fmt.Errorf("lower: bad locale: %w", err)
	}
	return cases.Lower(tag).String(s), nil
}

func initcap(text any, optLocale ...string) (any, error) {
	s, ok := nilOrString(text)
	if !ok {
		return nil, nil
	}
	tag := language.English
	if len(optLocale) > 0 {
		t, err := language.Parse(optLocale[0])
		if err != nil {
			return nil, fmt.Errorf("initcap: bad locale: %w", err)
		}
		tag = t
	}
	return cases.Title(tag).String(s), nil
}

func casefold(text any) any {
	s, ok := nilOrString(text)
	if !ok {
		return nil
	}
	return cases.Fold().String(s)
}

func unaccent(text any) (any, error) {
	s, ok := nilOrString(text)
	if !ok {
		return nil, nil
	}
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(gouni.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		return nil, fmt.Errorf("unaccent: %w", err)
	}
	return out, nil
}

func normalize(text any, optForm ...string) (any, error) {
	s, ok := nilOrString(text)
	if !ok {
		return nil, nil
	}
	form := norm.NFC
	if len(optForm) > 0 {
		switch strings.ToUpper(optForm[0]) {
		case "NFC":
			// already
		case "NFD":
			form = norm.NFD
		case "NFKC":
			form = norm.NFKC
		case "NFKD":
			form = norm.NFKD
		default:
			return nil, fmt.Errorf("normalize: invalid form %q (NFC / NFD / NFKC / NFKD)", optForm[0])
		}
	}
	return form.String(s), nil
}

// nilOrString returns (text, true) for any non-nil SQL value coerced to
// a Go string; (text, false) for nil input. Used by every scalar to
// keep NULL → NULL semantics clean.
func nilOrString(text any) (string, bool) {
	switch v := text.(type) {
	case nil:
		return "", false
	case string:
		return v, true
	case []byte:
		return string(v), true
	}
	return fmt.Sprint(text), true
}

// --- LIKE override (opt-in) ---

func likeTwo(pattern, text string) (bool, error) {
	return likeMatch(pattern, text, -1)
}

func likeThree(pattern, text, escape string) (bool, error) {
	if escape == "" {
		return likeMatch(pattern, text, -1)
	}
	r, size := utf8.DecodeRuneInString(escape)
	if size != len(escape) {
		return false, errors.New("LIKE ESCAPE expression must be a single character")
	}
	return likeMatch(pattern, text, r)
}

func likeMatch(pattern, text string, escape rune) (bool, error) {
	re, err := regexp.Compile(likeToRegex(pattern, escape))
	if err != nil {
		return false, fmt.Errorf("LIKE: %w", err)
	}
	return re.MatchString(text), nil
}

// likeToRegex translates an SQL LIKE pattern into an anchored RE2 pattern
// with the (?is) case-insensitive flag set. _ → ., % → .*. Characters
// after the ESCAPE character pass through literally.
func likeToRegex(pattern string, escape rune) string {
	var b strings.Builder
	b.Grow(len(pattern) + 10)
	b.WriteString(`(?is)\A`)
	literal := false
	start := 0
	for i, r := range pattern {
		if literal {
			literal = false
			// regexp.QuoteMeta the literal rune; cheaper to write
			// the byte directly since we know it's already lit.
			b.WriteString(regexp.QuoteMeta(string(r)))
			start = i + utf8.RuneLen(r)
			continue
		}
		var sym string
		switch r {
		case '_':
			sym = `.`
		case '%':
			sym = `.*`
		case escape:
			literal = true
			b.WriteString(regexp.QuoteMeta(pattern[start:i]))
			start = i + utf8.RuneLen(r)
			continue
		default:
			continue
		}
		b.WriteString(regexp.QuoteMeta(pattern[start:i]))
		b.WriteString(sym)
		start = i + utf8.RuneLen(r)
	}
	b.WriteString(regexp.QuoteMeta(pattern[start:]))
	b.WriteString(`\z`)
	return b.String()
}

// --- collations ---

// compareNoCase implements NOCASE_UNICODE — case-insensitive Unicode
// equality / ordering. Uses [strings.EqualFold]-style folding.
func compareNoCase(a, b string) int {
	return strings.Compare(strings.ToLower(a), strings.ToLower(b))
}

// compareNoCaseAccent implements NOCASE_ACCENT — case-insensitive PLUS
// accent-folded. Two strings that differ only by case and/or combining
// marks compare equal.
func compareNoCaseAccent(a, b string) int {
	la, _ := stripAccents(strings.ToLower(a))
	lb, _ := stripAccents(strings.ToLower(b))
	return strings.Compare(la, lb)
}

func stripAccents(s string) (string, error) {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(gouni.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	return out, err
}
