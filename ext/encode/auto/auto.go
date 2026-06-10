// Package auto wires the encode / decode scalar functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/encode/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/encode"
)

func init() {
	sqlite.RegisterAutoHook(encode.Register)
}
