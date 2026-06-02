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
	d := sqlite.DefaultDriver()
	prev := d.ConnectHook
	d.ConnectHook = func(c *sqlite.Conn) error {
		if prev != nil {
			if err := prev(c); err != nil {
				return err
			}
		}
		return bloom.Register(c)
	}
}
