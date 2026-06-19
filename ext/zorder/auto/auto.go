// Package auto wires the zorder extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers `zorder` and `unzorder`. Blank-import
// to opt in:
//
//	import _ "gosqlite.org/ext/zorder/auto"
//
// For explicit per-connection control (the canonical entry), call
// [gosqlite.org/ext/zorder.Register] directly.
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/zorder"
)

func init() {
	sqlite.RegisterAutoHook(zorder.Register)
}
