// Package auto wires the fileio extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers readfile, writefile, lsmode, and the
// fsdir vtab. Os-backed mode — for the sandboxed variant call
// [fileio.RegisterFS] explicitly. Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/fileio/auto"
//
// Security: blank-importing this package gives every SQL string executed
// against the driver pool full filesystem-read and filesystem-write
// access through readfile/writefile. Don't blank-import in services that
// run untrusted SQL.
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/fileio"
)

func init() {
	d := sqlite.DefaultDriver()
	prev := d.ConnectHook
	d.ConnectHook = func(c *sqlite.Conn) error {
		if prev != nil {
			if err := prev(c); err != nil {
				return err
			}
		}
		return fileio.Register(c)
	}
}
