// Package auto wires the ipaddr extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the IP / CIDR helpers. Blank-import to
// opt in:
//
//	import _ "github.com/go-again/sqlite/ext/ipaddr/auto"
package auto

import (
	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/ipaddr"
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
		return ipaddr.Register(c)
	}
}
