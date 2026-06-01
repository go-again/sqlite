// Package zorder provides z-order curve helpers for mapping
// multi-dimensional integer coordinates to a single ordered scalar
// (Morton encoding). Useful as the secondary key for a spatial-ish
// index built on a 1-D B-tree.
//
// # Functions
//
//   - zorder(d1, d2, …, dN) → INTEGER: interleaves the low bits of each
//     dimension into a single int64. 2 to 24 dimensions; each dimension
//     must fit in floor(63/N) bits, otherwise the function returns an
//     error.
//   - unzorder(z, N, i) → INTEGER: extracts dimension i (0-based) from a
//     z value that was encoded with N dimensions.
//
// Ported from [ncruces/ext/zorder] and the SQLite-bundled `zorder.c`.
// Pure Go, no external dependencies.
//
// # Usage
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/ext/zorder"
//	)
//
//	if err := zorder.Register(conn); err != nil { ... }
//	row := db.QueryRow(`SELECT zorder(10, 20, 30), unzorder(zorder(10, 20, 30), 3, 1)`)
//
// For pool-wide install via [github.com/go-again/sqlite.Driver.ConnectHook],
// blank-import the auto sub-package:
//
//	import _ "github.com/go-again/sqlite/ext/zorder/auto"
//
// [ncruces/ext/zorder]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/zorder
package zorder

import (
	"errors"
	"fmt"

	sqlite "github.com/go-again/sqlite"
)

// Register installs zorder + unzorder on c.
func Register(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc("zorder", encode, true),
		c.RegisterFunc("unzorder", decode, true),
	)
}

func encode(args ...int64) (int64, error) {
	n := len(args)
	if n < 2 || n > 24 {
		return 0, fmt.Errorf("zorder: needs between 2 and 24 dimensions, got %d", n)
	}
	x := make([]int64, n)
	copy(x, args)
	var z int64
	for i := range 63 {
		j := i % n
		z |= (x[j] & 1) << i
		x[j] >>= 1
	}
	for i := range n {
		if x[i] != 0 {
			return 0, fmt.Errorf("zorder: dimension %d overflows the available bit budget", i)
		}
	}
	return z, nil
}

func decode(z, n, i int64) (int64, error) {
	if n < 2 || n > 24 {
		return 0, fmt.Errorf("unzorder: needs between 2 and 24 dimensions, got %d", n)
	}
	if i < 0 || i >= n {
		return 0, fmt.Errorf("unzorder: index %d out of range [0, %d)", i, n)
	}
	if z < 0 {
		return 0, errors.New("unzorder: argument out of range (negative)")
	}
	var k int
	var x int64
	for j := i; j < 63; j += n {
		x |= ((z >> j) & 1) << k
		k++
	}
	return x, nil
}
