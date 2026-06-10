// Package auto wires the text_* scalar functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/text/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/text"
)

func init() {
	sqlite.RegisterAutoHook(text.Register)
}
