// Package auto wires the transitive_closure extension via a
// [sqlite.Driver.ConnectHook] so every new connection registers the
// vtab. Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/closure/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/closure"
)

func init() {
	sqlite.RegisterAutoHook(closure.Register)
}
