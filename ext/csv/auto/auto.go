// Package auto wires the csv extension via a [sqlite.Driver.ConnectHook]
// so every new connection auto-registers the `csv` module with os-backed
// file access. Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/csv/auto"
//
// For sandboxed filesystems (embed.FS / fstest.MapFS / os.DirFS), call
// [gosqlite.org/ext/csv.RegisterFS] from your own
// ConnectHook instead.
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/csv"
)

func init() {
	sqlite.RegisterAutoHook(csv.Register)
}
