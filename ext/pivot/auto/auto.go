// Package auto wires the pivot extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the `pivot` vtab. Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/pivot/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/pivot"
)

func init() {
	sqlite.RegisterAutoHook(pivot.Register)
}
