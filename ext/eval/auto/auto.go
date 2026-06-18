// Package auto wires the eval() function via a
// [sqlite.Driver.ConnectHook] so every new connection registers it.
// Blank-import to opt in — eval() runs arbitrary SQL, so only do this
// when every caller of eval() is trusted:
//
//	import _ "github.com/go-again/sqlite/ext/eval/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/eval"
)

func init() {
	sqlite.RegisterAutoHook(eval.Register)
}
