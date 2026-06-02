// Package auto wires the spellfix1 extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the `spellfix1` vtab. Blank-import
// to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/spellfix1/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/spellfix1"
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
		return spellfix1.Register(c)
	}
}
