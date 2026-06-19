// Package auto wires the fuzzy string-matching scalar functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/fuzzy/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/fuzzy"
)

func init() {
	sqlite.RegisterAutoHook(fuzzy.Register)
}
