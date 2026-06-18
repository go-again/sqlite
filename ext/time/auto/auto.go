// Package auto wires the time_* scalar functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/time/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	timeext "github.com/go-again/sqlite/ext/time"
)

func init() {
	sqlite.RegisterAutoHook(timeext.Register)
}
