// Package auto wires the transitive_closure extension via a
// [sqlite.Driver.ConnectHook] so every new connection registers the
// vtab. Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/closure/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/closure"
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
		return closure.Register(c)
	}
}
