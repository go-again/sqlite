// Package auto wires the generate_series table-valued function via a
// [sqlite.Driver.ConnectHook] so every new connection registers it.
// Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/series/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/series"
)

func init() {
	sqlite.RegisterAutoHook(series.Register)
}
