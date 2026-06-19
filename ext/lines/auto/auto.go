// Package auto wires the lines extension via [sqlite.Driver.ConnectHook]
// so every new connection registers the `lines` vtab module with
// os-backed file access. Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/lines/auto"
//
// For sandboxed deployments, call
// [gosqlite.org/ext/lines.RegisterFS] from your own
// ConnectHook instead.
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/lines"
)

func init() {
	sqlite.RegisterAutoHook(lines.Register)
}
