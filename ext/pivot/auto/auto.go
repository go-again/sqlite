// Package auto wires the pivot extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the `pivot` vtab. Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/pivot/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/pivot"
)

func init() {
	sqlite.RegisterAutoHook(pivot.Register)
}
