// Package auto wires the array extension via a [sqlite.Driver.ConnectHook]
// so every new connection auto-registers the `array` module. Blank-import
// to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/array/auto"
//
// For explicit per-connection control (the canonical entry), call
// [github.com/go-again/sqlite/ext/array.Register] directly.
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/array"
)

func init() {
	sqlite.RegisterAutoHook(array.Register)
}
