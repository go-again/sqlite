// Package auto wires the zorder extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers `zorder` and `unzorder`. Blank-import
// to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/zorder/auto"
//
// For explicit per-connection control (the canonical entry), call
// [github.com/go-again/sqlite/ext/zorder.Register] directly.
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/zorder"
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
		return zorder.Register(c)
	}
}
