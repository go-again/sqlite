// Package auto wires the lines extension via [sqlite.Driver.ConnectHook]
// so every new connection registers the `lines` vtab module with
// os-backed file access. Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/lines/auto"
//
// For sandboxed deployments, call
// [github.com/go-again/sqlite/ext/lines.RegisterFS] from your own
// ConnectHook instead.
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/lines"
)

func init() {
	sqlite.RegisterAutoHook(lines.Register)
}
