package sqlite // import "github.com/go-again/sqlite"

import (
	"errors"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// This file exposes SQLite's R-Tree custom-geometry and custom-query callback
// APIs (sqlite3_rtree_geometry_callback / sqlite3_rtree_query_callback) as
// typed (*Conn) methods. The rtree and geopoly virtual tables themselves are
// compiled into the underlying library and need no registration — these
// methods only add user-defined geometry/query functions usable with the
// R-Tree MATCH operator.
//
// The registration must live in this package (not ext/rtree) because it needs
// the connection's unexported tls/db handles — the same reason the wrapper is
// forked. The callbacks are dispatched through static trampolines plus an id
// registry, mirroring the scalar-UDF machinery in sqlite.go.

// RTreeGeometryFunc decides whether an R-Tree bounding box is a candidate for
// a custom-geometry MATCH query. It is called for every node SQLite visits,
// internal and leaf.
//
// coords is the node's bounding box as [min0, max0, min1, max1, …] — two
// values per R-Tree dimension. params holds the arguments passed to the
// geometry function in SQL: for `WHERE id MATCH mygeom(1, 2, 3)`, params is
// [1, 2, 3]. Return true if the box overlaps the query region (the subtree or
// row is a candidate), false to prune it.
type RTreeGeometryFunc func(coords, params []float64) (overlap bool, err error)

// RTreeWithin classifies how a node's bounding box relates to the query
// region. It is the result an [RTreeQueryFunc] assigns to each visited node.
type RTreeWithin int32

const (
	// RTreeNotWithin prunes the box: neither it nor its descendants match.
	RTreeNotWithin RTreeWithin = 0
	// RTreePartlyWithin keeps the box as a candidate; children/rows are
	// re-checked.
	RTreePartlyWithin RTreeWithin = 1
	// RTreeFullyWithin reports the box entirely inside the query region; every
	// descendant row matches without a further callback.
	RTreeFullyWithin RTreeWithin = 2
)

// RTreeQueryInfo is the per-node state passed to an [RTreeQueryFunc]. It is
// the richer alternative to [RTreeGeometryFunc]: the callback sees tree
// position and parent classification, and assigns both a within-result and a
// visit-order score.
type RTreeQueryInfo struct {
	// Coords is the node bounding box as [min0, max0, min1, max1, …].
	Coords []float64
	// Params are the arguments to the SQL query function (e.g. the radius and
	// centre of a circle).
	Params []float64
	// Level is the current node's height above the leaves; Leaf reports
	// whether Level == 0 (a row, not an internal node).
	Level int
	// MaxLevel is the height of the whole tree.
	MaxLevel int
	// Rowid is the row identifier; meaningful only at the leaf level.
	Rowid int64
	// ParentScore is the score the callback assigned to this node's parent.
	ParentScore float64
	// ParentWithin is the classification the callback gave the parent.
	ParentWithin RTreeWithin
}

// Leaf reports whether this node is a leaf (a stored row) rather than an
// internal bounding box.
func (q *RTreeQueryInfo) Leaf() bool { return q.Level == 0 }

// RTreeQueryFunc classifies a node against the query region and assigns it a
// score. Nodes are visited in ascending score order, so a smaller score is
// explored first. Return [RTreeNotWithin] to prune.
type RTreeQueryFunc func(info *RTreeQueryInfo) (within RTreeWithin, score float64, err error)

var (
	rtreeGeom  = newCallbackTable[RTreeGeometryFunc]()
	rtreeQuery = newCallbackTable[RTreeQueryFunc]()
)

// RegisterRTreeGeometry registers fn as a custom R-Tree geometry function
// named name, usable as `WHERE <rtree>.id MATCH name(…)`. See
// [RTreeGeometryFunc].
//
// Wraps sqlite3_rtree_geometry_callback:
// https://sqlite.org/rtree.html#custom_r_tree_queries
func (c *Conn) RegisterRTreeGeometry(name string, fn RTreeGeometryFunc) error {
	if fn == nil {
		return errors.New("sqlite: RegisterRTreeGeometry: nil function")
	}
	cName, err := libc.CString(name)
	if err != nil {
		return err
	}
	defer libc.Xfree(c.tls, cName)

	id := rtreeGeom.register(fn)
	rc := sqlite3.Xsqlite3_rtree_geometry_callback(c.tls, c.db, cName, cFuncPointer(rtreeGeomTrampoline), id)
	if rc != sqlite3.SQLITE_OK {
		rtreeGeom.drop(id)
		return c.errstr(rc)
	}
	// Track the id so (*conn).Close reclaims it; otherwise the closure and its
	// captured *libc.TLS leak for the process lifetime (and the registry grows
	// unboundedly under per-connection ConnectHook registration).
	c.rtreeGeomIDs = append(c.rtreeGeomIDs, id)
	return nil
}

// RegisterRTreeQuery registers fn as a custom R-Tree query function named
// name. It is the second-generation, more expressive form of
// [RegisterRTreeGeometry]: fn sees the node's tree position and assigns a
// within-classification plus a visit-order score. See [RTreeQueryFunc].
//
// Wraps sqlite3_rtree_query_callback:
// https://sqlite.org/rtree.html#custom_r_tree_queries
func (c *Conn) RegisterRTreeQuery(name string, fn RTreeQueryFunc) error {
	if fn == nil {
		return errors.New("sqlite: RegisterRTreeQuery: nil function")
	}
	cName, err := libc.CString(name)
	if err != nil {
		return err
	}
	defer libc.Xfree(c.tls, cName)

	id := rtreeQuery.register(fn)
	rc := sqlite3.Xsqlite3_rtree_query_callback(c.tls, c.db, cName, cFuncPointer(rtreeQueryTrampoline), id, 0)
	if rc != sqlite3.SQLITE_OK {
		rtreeQuery.drop(id)
		return c.errstr(rc)
	}
	// Track the id so (*conn).Close reclaims it (see RegisterRTreeGeometry).
	c.rtreeQueryIDs = append(c.rtreeQueryIDs, id)
	return nil
}

// rtreeGeomTrampoline is the C-callable entry point for every registered
// geometry function. It recovers the Go closure from the id SQLite stashed in
// the geometry context, marshals the coordinate arrays into Go-owned slices,
// and writes the overlap result back through pRes.
//
// Signature matches the transpiled xGeom:
//
//	int (*)(sqlite3_rtree_geometry*, int nCoord, sqlite3_rtree_dbl *aCoord, int *pRes)
func rtreeGeomTrampoline(tls *libc.TLS, pGeom uintptr, nCoord int32, aCoord uintptr, pRes uintptr) int32 {
	g := (*sqlite3.Tsqlite3_rtree_geometry)(unsafe.Pointer(pGeom))
	fn, ok := rtreeGeom.lookup(g.FpContext)
	if !ok {
		return int32(sqlite3.SQLITE_ERROR)
	}
	overlap, err := fn(copyCDoubles(aCoord, int(nCoord)), copyCDoubles(g.FaParam, int(g.FnParam)))
	if err != nil {
		return int32(sqlite3.SQLITE_ERROR)
	}
	res := int32(0)
	if overlap {
		res = 1
	}
	*(*int32)(unsafe.Pointer(pRes)) = res
	return int32(sqlite3.SQLITE_OK)
}

// rtreeQueryTrampoline is the C-callable entry point for every registered
// query function. Signature matches the transpiled xQueryFunc:
//
//	int (*)(sqlite3_rtree_query_info*)
func rtreeQueryTrampoline(tls *libc.TLS, pInfo uintptr) int32 {
	qi := (*sqlite3.Tsqlite3_rtree_query_info)(unsafe.Pointer(pInfo))
	fn, ok := rtreeQuery.lookup(qi.FpContext)
	if !ok {
		return int32(sqlite3.SQLITE_ERROR)
	}
	info := &RTreeQueryInfo{
		Coords:       copyCDoubles(qi.FaCoord, int(qi.FnCoord)),
		Params:       copyCDoubles(qi.FaParam, int(qi.FnParam)),
		Level:        int(qi.FiLevel),
		MaxLevel:     int(qi.FmxLevel),
		Rowid:        int64(qi.FiRowid),
		ParentScore:  float64(qi.FrParentScore),
		ParentWithin: RTreeWithin(qi.FeParentWithin),
	}
	within, score, err := fn(info)
	if err != nil {
		return int32(sqlite3.SQLITE_ERROR)
	}
	qi.FeWithin = int32(within)
	qi.FrScore = float64(score)
	return int32(sqlite3.SQLITE_OK)
}

// copyCDoubles reads n sqlite3_rtree_dbl (float64) values from the C array at
// ptr into a fresh Go slice, so the user callback can hold onto it without
// retaining a pointer into SQLite-owned memory.
func copyCDoubles(ptr uintptr, n int) []float64 {
	if ptr == 0 || n <= 0 {
		return nil
	}
	out := make([]float64, n)
	copy(out, unsafe.Slice((*float64)(unsafe.Pointer(ptr)), n))
	return out
}

// compile-time guards that the trampolines match the signatures the
// transpiled rtree callback registrars expect.
var (
	_ func(*libc.TLS, uintptr, int32, uintptr, uintptr) int32 = rtreeGeomTrampoline
	_ func(*libc.TLS, uintptr) int32                          = rtreeQueryTrampoline
)
