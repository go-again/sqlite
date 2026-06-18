// Package auto wires the decimal_* scalar + aggregate functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/decimal/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/decimal"
)

func init() {
	sqlite.RegisterAutoHook(decimal.Register)
}
