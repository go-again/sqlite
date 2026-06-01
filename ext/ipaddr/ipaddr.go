// Package ipaddr provides typed IP / CIDR helper functions for SQL queries.
// Pure Go via [net/netip]; no external dependencies.
//
// # Functions
//
//   - ipcontains(prefix, ip) → BOOL: true if prefix contains ip
//   - ipoverlaps(prefix1, prefix2) → BOOL: true if the two prefixes overlap
//   - ipfamily(ip-or-prefix) → INTEGER (4 or 6)
//   - iphost(ip-or-prefix) → TEXT: canonical address form
//   - ipmasklen(prefix) → INTEGER: prefix bit count
//   - ipnetwork(prefix) → TEXT: the prefix's masked network address
//
// Ported from [ncruces/ext/ipaddr] with one upstream bug fix: ipoverlaps
// in ncruces parses arg[0] twice (lines 50-51 of upstream ipaddr.go),
// effectively comparing prefix1 to itself. Our version reads each prefix
// from its own argument, matching the function's documented semantics.
//
// # Usage
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/ext/ipaddr"
//	)
//
//	if err := ipaddr.Register(conn); err != nil { ... }
//
//	rows, _ := db.QueryContext(ctx,
//	    `SELECT ip FROM events WHERE ipcontains('10.0.0.0/8', ip)`)
//
// For pool-wide install via [github.com/go-again/sqlite.Driver.ConnectHook],
// blank-import the auto sub-package:
//
//	import _ "github.com/go-again/sqlite/ext/ipaddr/auto"
//
// [ncruces/ext/ipaddr]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/ipaddr
package ipaddr

import (
	"errors"
	"fmt"
	"net/netip"

	sqlite "github.com/go-again/sqlite"
)

// Register installs all six functions on c.
func Register(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc("ipcontains", contains, true),
		c.RegisterFunc("ipoverlaps", overlaps, true),
		c.RegisterFunc("ipfamily", family, true),
		c.RegisterFunc("iphost", host, true),
		c.RegisterFunc("ipmasklen", masklen, true),
		c.RegisterFunc("ipnetwork", network, true),
	)
}

func contains(prefixStr, ipStr string) (bool, error) {
	prefix, err := netip.ParsePrefix(prefixStr)
	if err != nil {
		return false, fmt.Errorf("ipcontains: bad prefix: %w", err)
	}
	addr, err := netip.ParseAddr(ipStr)
	if err != nil {
		return false, fmt.Errorf("ipcontains: bad addr: %w", err)
	}
	return prefix.Contains(addr), nil
}

func overlaps(prefix1Str, prefix2Str string) (bool, error) {
	prefix1, err := netip.ParsePrefix(prefix1Str)
	if err != nil {
		return false, fmt.Errorf("ipoverlaps: bad prefix1: %w", err)
	}
	prefix2, err := netip.ParsePrefix(prefix2Str)
	if err != nil {
		return false, fmt.Errorf("ipoverlaps: bad prefix2: %w", err)
	}
	return prefix1.Overlaps(prefix2), nil
}

func family(s string) (int64, error) {
	addr, err := resolveAddr(s)
	if err != nil {
		return 0, fmt.Errorf("ipfamily: %w", err)
	}
	switch {
	case addr.Is4():
		return 4, nil
	case addr.Is6():
		return 6, nil
	default:
		return 0, errors.New("ipfamily: address has no IP family")
	}
}

func host(s string) (string, error) {
	addr, err := resolveAddr(s)
	if err != nil {
		return "", fmt.Errorf("iphost: %w", err)
	}
	return addr.String(), nil
}

func masklen(s string) (int64, error) {
	prefix, err := netip.ParsePrefix(s)
	if err != nil {
		return 0, fmt.Errorf("ipmasklen: %w", err)
	}
	return int64(prefix.Bits()), nil
}

func network(s string) (string, error) {
	prefix, err := netip.ParsePrefix(s)
	if err != nil {
		return "", fmt.Errorf("ipnetwork: %w", err)
	}
	return prefix.Masked().String(), nil
}

// resolveAddr accepts a bare address, a prefix (returns the addr part),
// or an "addr:port" form (returns the addr part). Matches the upstream
// permissiveness; iphost/ipfamily callers don't have to know the input shape.
func resolveAddr(s string) (netip.Addr, error) {
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr, nil
	}
	if prefix, err := netip.ParsePrefix(s); err == nil {
		return prefix.Addr(), nil
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr(), nil
	}
	return netip.Addr{}, fmt.Errorf("not a valid address / prefix / addr:port: %q", s)
}
