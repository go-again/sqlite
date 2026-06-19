// Package auto wires the blobio extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers readblob and writeblob. Blank-import
// to opt in:
//
//	import _ "gosqlite.org/ext/blobio/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/blobio"
)

func init() {
	sqlite.RegisterAutoHook(blobio.Register)
}
