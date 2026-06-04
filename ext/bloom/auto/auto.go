// Package auto wires the bloom extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the `bloom` vtab module. Blank-import
// to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/bloom/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/bloom"
)

func init() {
	sqlite.RegisterAutoHook(bloom.Register)
}
