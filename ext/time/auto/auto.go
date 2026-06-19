// Package auto wires the time_* scalar functions via a
// [sqlite.Driver.ConnectHook] so every new connection registers them.
// Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/time/auto"
package auto

import (
	sqlite "gosqlite.org"
	timeext "gosqlite.org/ext/time"
)

func init() {
	sqlite.RegisterAutoHook(timeext.Register)
}
