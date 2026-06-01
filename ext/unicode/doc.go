// Package unicode provides Unicode-aware string SQL functions and
// collations as an alternative to the SQLite ICU extension. Pure Go
// over [golang.org/x/text].
//
// # Functions
//
//   - upper(text [, locale]) — Unicode upper-case (locale-aware when given)
//   - lower(text [, locale]) — Unicode lower-case
//   - initcap(text [, locale]) — title-case each word
//   - casefold(text) — Unicode case folding (correct for case-insensitive eq)
//   - normalize(text [, form]) — NFC (default) / NFD / NFKC / NFKD
//   - unaccent(text) — strip combining marks (NFD → remove unicode.Mn → NFC)
//
// All scalar functions are deterministic.
//
// # Collations
//
// [Register] installs two preset collations on every call:
//
//   - NOCASE_UNICODE — case-insensitive Unicode comparison (the default
//     SQLite NOCASE collation is ASCII-only).
//   - NOCASE_ACCENT — NOCASE_UNICODE plus accent-folding via [unaccent].
//
// For language-tagged collations (e.g. ordering Turkish 'i' / 'İ'
// correctly), call [RegisterLocaleCollation]:
//
//	unicode.RegisterLocaleCollation(conn, "tr", "TR")
//	// Now: ORDER BY name COLLATE TR
//
// # The LIKE override
//
// SQLite ships an ASCII-only LIKE built-in. Registering a Unicode-aware
// LIKE override disables SQLite's LIKE optimization (the planner can no
// longer rewrite `col LIKE 'prefix%'` into `col >= 'prefix' AND col <
// 'prefiy'` — see https://sqlite.org/optoverview.html#the_like_optimization).
//
// For that reason [Register] does NOT install the LIKE override by
// default. To opt in:
//
//	unicode.RegisterLike = true
//	unicode.Register(conn)
//
// Or call [RegisterLikeOnly] separately.
//
// The Unicode LIKE follows [strings.EqualFold] semantics — a permissive
// case-insensitive match that handles most real-world Unicode cases
// correctly, with documented gaps around Turkish-specific case folding
// (Turkish 'i' / 'İ' / 'I' / 'ı'). For Turkish-specific semantics,
// register a language-tagged collation instead.
//
// # Usage
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/ext/unicode"
//	)
//
//	if err := unicode.Register(conn); err != nil { ... }
//
//	row := db.QueryRow(`SELECT casefold(?), unaccent(?)`, "GROẞER", "café")
//
// For pool-wide install via [github.com/go-again/sqlite.Driver.ConnectHook],
// blank-import the auto sub-package:
//
//	import _ "github.com/go-again/sqlite/ext/unicode/auto"
//
// # Compatibility notes
//
// Ported from [ncruces/ext/unicode]. The function lineup matches; the
// REGEXP override is intentionally omitted because [ext/regexp] already
// surfaces a richer Unicode-aware REGEXP operator and we don't want
// silent function-name conflicts.
//
// Expect small differences from the SQLite ICU extension and PostgreSQL
// in edge cases:
//
//   - Locale-aware casing (Turkish dotless 'ı', German eszett 'ß' →
//     "ss" / "SS" in upper) follows [golang.org/x/text/cases]. The
//     two-argument upper/lower/initcap forms accept a [language.Tag]
//     string.
//   - casefold uses [cases.Fold] — the right answer for case-insensitive
//     equality checks (e.g. distinguishes Greek final-sigma 'ς' from
//     non-final 'σ' before folding both to lower).
//   - unaccent decomposes via NFD, drops combining marks, recomposes via
//     NFC. Logographic / non-Latin scripts pass through unchanged.
//
// [ncruces/ext/unicode]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/unicode
package unicode
