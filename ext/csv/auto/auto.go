// Package auto wires the csv extension via a [sqlite.Driver.ConnectHook]
// so every new connection auto-registers the `csv` module with os-backed
// file access. Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/csv/auto"
//
// For sandboxed filesystems (embed.FS / fstest.MapFS / os.DirFS), call
// [github.com/go-again/sqlite/ext/csv.RegisterFS] from your own
// ConnectHook instead.
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/csv"
)

func init() {
	sqlite.RegisterAutoHook(csv.Register)
}
