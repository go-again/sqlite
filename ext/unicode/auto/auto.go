// Package auto wires the unicode extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the Unicode-aware scalar functions
// plus the NOCASE_UNICODE / NOCASE_ACCENT collations. Blank-import to
// opt in:
//
//	import _ "github.com/go-again/sqlite/ext/unicode/auto"
//
// The LIKE override is NOT installed by this auto wiring — leaving SQLite's
// LIKE optimization intact. To opt in, set
// [github.com/go-again/sqlite/ext/unicode.RegisterLike] to true BEFORE
// the first connection opens, or call
// [github.com/go-again/sqlite/ext/unicode.RegisterLikeOnly] from your
// own ConnectHook.
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/unicode"
)

func init() {
	d := sqlite.DefaultDriver()
	prev := d.ConnectHook
	d.ConnectHook = func(c *sqlite.Conn) error {
		if prev != nil {
			if err := prev(c); err != nil {
				return err
			}
		}
		return unicode.Register(c)
	}
}
