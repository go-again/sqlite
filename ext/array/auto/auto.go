// Package auto wires the array extension via a [sqlite.Driver.ConnectHook]
// so every new connection auto-registers the `array` module. Blank-import
// to opt in:
//
//	import _ "gosqlite.org/ext/array/auto"
//
// For explicit per-connection control (the canonical entry), call
// [gosqlite.org/ext/array.Register] directly.
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/array"
)

func init() {
	sqlite.RegisterAutoHook(array.Register)
}
