// Package auto wires the rtree geometry functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/rtree/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/rtree"
)

func init() {
	sqlite.RegisterAutoHook(rtree.Register)
}
