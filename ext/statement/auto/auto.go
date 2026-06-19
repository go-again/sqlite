// Package auto wires the statement extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the `statement` vtab. Blank-import
// to opt in:
//
//	import _ "gosqlite.org/ext/statement/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/statement"
)

func init() {
	sqlite.RegisterAutoHook(statement.Register)
}
