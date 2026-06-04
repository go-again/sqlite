// Package auto wires the unicode extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the Unicode-aware scalar functions
// plus the NOCASE_UNICODE / NOCASE_ACCENT collations. Blank-import to
// opt in:
//
//	import _ "github.com/go-again/sqlite/ext/unicode/auto"
//
// The LIKE override is NOT installed by this auto wiring — leaving
// SQLite's LIKE optimization intact. To opt in, install via your own
// ConnectHook with [github.com/go-again/sqlite/ext/unicode.WithLike]:
//
//	unicode.Register(conn, unicode.WithLike())
//
// Or call [github.com/go-again/sqlite/ext/unicode.RegisterLikeOnly]
// separately.
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/unicode"
)

func init() {
	sqlite.RegisterAutoHook(func(c *sqlite.Conn) error {
		// unicode.Register is variadic; the auto path takes no opts.
		return unicode.Register(c)
	})
}
