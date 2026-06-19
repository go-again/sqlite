// Package rtree adds ready-made custom geometry callbacks for SQLite's
// built-in R-Tree module, layered on
// [gosqlite.org.Conn.RegisterRTreeGeometry].
//
// The `rtree` and `geopoly` virtual tables are compiled into the underlying
// library and need no registration — `CREATE VIRTUAL TABLE … USING rtree(…)`
// works out of the box. This package only installs user geometry functions
// usable with the R-Tree MATCH operator:
//
//	CREATE VIRTUAL TABLE demo USING rtree(id, minX, maxX, minY, maxY);
//	-- rows whose box overlaps the circle of radius 5 centred at (10, 10):
//	SELECT id FROM demo WHERE id MATCH circle(10, 10, 5);
//
// For arbitrary custom geometry, call
// [gosqlite.org.Conn.RegisterRTreeGeometry] (single bounding-box
// overlap test) or [gosqlite.org.Conn.RegisterRTreeQuery] (the
// richer query-callback form) directly.
//
// For a typed handle that hides the `CREATE VIRTUAL TABLE … USING rtree(…)`
// column list and the overlap-predicate SQL — Create / Insert / InsertPoint /
// Search / SearchCircle / Delete / Drop — see [Table].
//
// # Usage
//
//	import (
//	    sqlite "gosqlite.org"
//	    "gosqlite.org/ext/rtree"
//	)
//
//	if err := rtree.Register(conn); err != nil { ... }
//
// Geometry functions are per-connection. For pool-wide install, blank-import
// the auto sub-package:
//
//	import _ "gosqlite.org/ext/rtree/auto"
package rtree

import (
	"fmt"

	sqlite "gosqlite.org"
)

// Register installs this package's geometry functions on c:
//
//   - circle(cx, cy, r): matches rows of a 2D R-Tree whose bounding box
//     overlaps the circle of radius r centred at (cx, cy).
func Register(c *sqlite.Conn) error {
	return c.RegisterRTreeGeometry("circle", circle)
}

// circle reports whether the 2D bounding box coords ([minX, maxX, minY, maxY])
// overlaps the circle params=[cx, cy, r]. The test compares the box's nearest
// point to the centre against the radius, so it is exact for both internal
// nodes (descend when the subtree box reaches the circle) and leaf rows.
func circle(coords, params []float64) (bool, error) {
	if len(coords) < 4 {
		return false, fmt.Errorf("rtree.circle: need a 2D rtree (>=4 coords), got %d", len(coords))
	}
	if len(params) != 3 {
		return false, fmt.Errorf("rtree.circle: want circle(cx, cy, r), got %d args", len(params))
	}
	cx, cy, r := params[0], params[1], params[2]
	dx := cx - clamp(cx, coords[0], coords[1])
	dy := cy - clamp(cy, coords[2], coords[3])
	return dx*dx+dy*dy <= r*r, nil
}

// clamp returns v confined to [lo, hi]; for a box edge pair that yields the
// box's coordinate nearest to v.
func clamp(v, lo, hi float64) float64 {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}
