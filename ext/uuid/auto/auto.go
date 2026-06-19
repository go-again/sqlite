// Package auto wires the uuid extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the UUID SQL functions. Blank-import
// to opt in:
//
//	import _ "gosqlite.org/ext/uuid/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/uuid"
)

func init() {
	sqlite.RegisterAutoHook(uuid.Register)
}
