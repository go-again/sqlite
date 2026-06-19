// Package auto wires the money_* scalar functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/money/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/money"
)

func init() {
	sqlite.RegisterAutoHook(money.Register)
}
