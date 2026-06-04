// Package auto wires the stats extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the statistical aggregate / window
// function lineup. Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/stats/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/stats"
)

func init() {
	sqlite.RegisterAutoHook(stats.Register)
}
