// Package auto wires the blobio extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers readblob and writeblob. Blank-import
// to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/blobio/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/blobio"
)

func init() {
	sqlite.RegisterAutoHook(blobio.Register)
}
