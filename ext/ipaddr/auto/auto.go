// Package auto wires the ipaddr extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the IP / CIDR helpers. Blank-import to
// opt in:
//
//	import _ "gosqlite.org/ext/ipaddr/auto"
package auto

import (
	sqlite "gosqlite.org"
	"gosqlite.org/ext/ipaddr"
)

func init() {
	sqlite.RegisterAutoHook(ipaddr.Register)
}
