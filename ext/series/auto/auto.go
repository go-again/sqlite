// Package auto wires the generate_series table-valued function via a
// [sqlite.Driver.ConnectHook] so every new connection registers it.
// Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/series/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/series"
)

func init() {
	sqlite.RegisterAutoHook(series.Register)
}
