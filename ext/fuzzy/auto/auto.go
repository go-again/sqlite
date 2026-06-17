// Package auto wires the fuzzy string-matching scalar functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/fuzzy/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/fuzzy"
)

func init() {
	sqlite.RegisterAutoHook(fuzzy.Register)
}
