// Package auto wires the regexp extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the REGEXP operator and the regexp_*
// SQL function family. Blank-import to opt in:
//
//	import _ "gosqlite.org/ext/regexp/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/regexp"
)

func init() {
	sqlite.RegisterAutoHook(regexp.Register)
}
