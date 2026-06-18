// Package auto wires the money_* scalar functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/money/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/money"
)

func init() {
	sqlite.RegisterAutoHook(money.Register)
}
