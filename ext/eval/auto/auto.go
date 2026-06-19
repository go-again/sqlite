// Package auto wires the eval() function via a
// [sqlite.Driver.ConnectHook] so every new connection registers it.
// Blank-import to opt in — eval() runs arbitrary SQL, so only do this
// when every caller of eval() is trusted:
//
//	import _ "gosqlite.org/ext/eval/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/eval"
)

func init() {
	sqlite.RegisterAutoHook(eval.Register)
}
