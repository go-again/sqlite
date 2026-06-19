// Package auto wires the spellfix1 extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the `spellfix1` vtab. Blank-import
// to opt in:
//
//	import _ "gosqlite.org/ext/spellfix1/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/spellfix1"
)

func init() {
	sqlite.RegisterAutoHook(spellfix1.Register)
}
