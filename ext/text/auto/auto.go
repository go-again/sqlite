// Package auto wires the text_* scalar functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/text/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/text"
)

func init() {
	sqlite.RegisterAutoHook(text.Register)
}
