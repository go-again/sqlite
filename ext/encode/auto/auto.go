// Package auto wires the encode / decode scalar functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/encode/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/encode"
)

func init() {
	sqlite.RegisterAutoHook(encode.Register)
}
