// Package auto wires the decimal_* scalar + aggregate functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/decimal/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/decimal"
)

func init() {
	sqlite.RegisterAutoHook(decimal.Register)
}
