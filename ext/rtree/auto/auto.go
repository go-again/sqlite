// Package auto wires the rtree geometry functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/rtree/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/rtree"
)

func init() {
	sqlite.RegisterAutoHook(rtree.Register)
}
