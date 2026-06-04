// Package auto wires the uuid extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the UUID SQL functions. Blank-import
// to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/uuid/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/uuid"
)

func init() {
	sqlite.RegisterAutoHook(uuid.Register)
}
