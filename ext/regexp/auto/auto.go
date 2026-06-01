// Package auto wires the regexp extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the REGEXP operator and the regexp_*
// SQL function family. Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/regexp/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/regexp"
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
		return regexp.Register(c)
	}
}
