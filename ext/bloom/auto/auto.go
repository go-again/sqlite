// Package auto wires the bloom extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the `bloom` vtab module. Blank-import
// to opt in:
//
//	import _ "gosqlite.org/ext/bloom/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/bloom"
)

func init() {
	sqlite.RegisterAutoHook(bloom.Register)
}
