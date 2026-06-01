// Package array provides the `array` table-valued SQL function — a way to
// feed a Go slice into a SQL query as a single-column table.
//
// The vtab is a Go-native re-implementation of SQLite's bundled carray
// (https://sqlite.org/carray.html) and the equivalent ncruces/ext/array
// module.
//
// # Transparent binding (preferred)
//
// Wrap the slice with [sqlite.Pointer] and pass it as a regular query
// argument. SQLite's destructor callback releases the binding when the
// statement finalizes — no caller-side cleanup needed.
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/ext/array"
//	)
//
//	if err := array.Register(conn); err != nil { ... }
//
//	rows, _ := db.QueryContext(ctx,
//	    `SELECT value FROM array(?) ORDER BY value`,
//	    sqlite.Pointer([]int{10, 20, 30}))
//
// # Explicit Bind / Release (escape hatch)
//
// For long-lived bindings (same slice across many queries) or when an
// int64 sentinel is more convenient than a wrapped argument, the explicit
// pair stays available:
//
//	token, release := array.Bind(conn, []int{10, 20, 30})
//	defer release()
//	rows, _ := db.QueryContext(ctx,
//	    `SELECT value FROM array(?) ORDER BY value`, token)
//
// # Supported element types
//
// Bind accepts any of:
//
//   - []int, []int8, []int16, []int32, []int64
//   - []uint8 (treated as a single BLOB), []uint16, []uint32, []uint64
//   - []float32, []float64
//   - []bool
//   - []string
//   - [][]byte
//   - []any (each element coerced as above; nil becomes SQL NULL)
//
// Anything else is reflect-walked; element kinds not in the list above
// surface a clear MISMATCH error from the cursor.
//
// # Blank-import auto-registration
//
// For a pool-wide install via [github.com/go-again/sqlite.Driver.ConnectHook],
// blank-import the auto sub-package:
//
//	import _ "github.com/go-again/sqlite/ext/array/auto"
package array

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"

	sqlite "github.com/go-again/sqlite"
)

// ModuleName is the SQL module name the vtab registers under: `array`.
const ModuleName = "array"

// ErrUnknownToken is returned by the array vtab cursor when Filter receives
// a token that has no corresponding [Bind] entry (typically because the
// binding was already released or the token was forged).
var ErrUnknownToken = errors.New("array: unknown token; was the binding already released?")

// Register installs the array module on c as an eponymous virtual table —
// callers can `SELECT … FROM array(?)` directly without a preceding CREATE
// VIRTUAL TABLE. Idempotent at the SQLite level; re-registering the same
// module name on the same connection is a no-op the driver tolerates.
func Register(c *sqlite.Conn) error {
	return c.CreateEponymousModule(ModuleName, ctor)
}

// Bind registers slice for use with the array vtab and returns a token plus
// a release function. The token must be passed as the array() argument in
// the SQL:
//
//	token, release := array.Bind(conn, my[]int{...})
//	defer release()
//	rows, _ := db.Query(`SELECT value FROM array(?)`, token)
//
// release frees the binding; calling it more than once is a no-op. Failing
// to release leaks the entry until process exit (the binding is held in a
// process-global map keyed by token). For long-running services, ALWAYS
// defer release immediately after Bind.
//
// slice must be a slice / array / pointer-to-array of one of the supported
// element types listed in the package doc. Bind itself does not validate
// — invalid kinds surface as a MISMATCH error from the cursor's Column.
func Bind(c *sqlite.Conn, slice any) (token int64, release func()) {
	id := bindings.Next()
	bindings.store(id, slice)
	var once sync.Once
	return id, func() {
		once.Do(func() { bindings.delete(id) })
	}
}

// ctor is the [sqlite.VTabCtor] invoked by xCreate and xConnect. It declares
// the table schema (one visible value column, one HIDDEN array constraint
// column) and returns a stateless table — all per-query state lives on the
// cursor.
func ctor(c *sqlite.Conn, _, _, _ string, _ []string) (sqlite.VTab, error) {
	if err := c.DeclareVTab(`CREATE TABLE x(value, array HIDDEN)`); err != nil {
		return nil, err
	}
	return arrayTable{}, nil
}

type arrayTable struct{}

// BestIndex consumes the hidden "array" column constraint when it appears
// as `array = ?` so SQLite routes the bound token into [arrayCursor.Filter]
// as argv[0]. Without a usable constraint we return SQLITE_CONSTRAINT so
// the planner reports a clear error to the caller instead of an empty
// table — matches the SQLite-bundled carray semantics.
func (arrayTable) BestIndex(info *sqlite.IndexInfo) error {
	for i, cst := range info.Constraints {
		// Column index 1 is "array HIDDEN" (0 is "value").
		if cst.Column == 1 && cst.Op == sqlite.OpEQ && cst.Usable {
			info.Constraints[i].ArgIndex = 0
			info.Constraints[i].Omit = true
			info.EstimatedCost = 1
			info.EstimatedRows = 100
			return nil
		}
	}
	// Returning a plain error pins the failure mode; SQLite surfaces it
	// to the caller. (modernc/sqlite/vtab does not export a typed
	// CONSTRAINT sentinel today.)
	return errors.New("array: missing required `array = ?` constraint")
}

func (arrayTable) Open() (sqlite.VTabCursor, error) { return &arrayCursor{}, nil }
func (arrayTable) Disconnect() error                { return nil }
func (arrayTable) Destroy() error                   { return nil }

// arrayCursor walks the slice the Filter call resolved from its token.
type arrayCursor struct {
	value  reflect.Value // the bound slice / array, indexable
	rawAny any           // the original argument, retained for typed fast paths
	rowID  int           // 0-based position
	length int
}

func (c *arrayCursor) Filter(_ int, _ string, args []sqlite.Value) error {
	if len(args) == 0 {
		return errors.New("array: Filter called without the array() argument")
	}
	arg := args[0]
	var slice any
	switch x := arg.(type) {
	case int64:
		// Explicit Bind / Release path: the argument is a token into
		// the per-package registry.
		v, ok := bindings.load(x)
		if !ok {
			return ErrUnknownToken
		}
		slice = v
	case nil:
		return errors.New("array: array(NULL) is not a valid binding")
	default:
		// Transparent path: sqlite.Pointer substituted the original
		// Go value into the args slot for us.
		slice = x
	}
	rv := reflect.ValueOf(slice)
	indexable, err := makeIndexable(rv)
	if err != nil {
		return err
	}
	c.rawAny = slice
	c.value = indexable
	c.length = indexable.Len()
	c.rowID = 0
	return nil
}

func (c *arrayCursor) Next() error           { c.rowID++; return nil }
func (c *arrayCursor) Eof() bool             { return c.rowID >= c.length }
func (c *arrayCursor) Rowid() (int64, error) { return int64(c.rowID) + 1, nil } // 1-based, matches carray.
func (c *arrayCursor) Close() error          { c.value = reflect.Value{}; c.rawAny = nil; return nil }

// Column resolves the value column (n=0). Column 1 is the HIDDEN array
// constraint — SQLite never asks us to materialize it because BestIndex
// set Omit=true, but we tolerate the call by returning nil.
func (c *arrayCursor) Column(n int) (sqlite.Value, error) {
	if n != 0 {
		return nil, nil
	}
	switch arr := c.rawAny.(type) {
	case []int:
		return int64(arr[c.rowID]), nil
	case []int64:
		return arr[c.rowID], nil
	case []float64:
		return arr[c.rowID], nil
	case []string:
		return arr[c.rowID], nil
	case []bool:
		return arr[c.rowID], nil
	case [][]byte:
		return arr[c.rowID], nil
	}
	v := c.value.Index(c.rowID)
	k := v.Kind()
	if k == reflect.Interface {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
		k = v.Kind()
	}
	switch {
	case v.CanInt():
		return v.Int(), nil
	case v.CanUint():
		u := v.Uint()
		if u > (1<<63 - 1) {
			return nil, fmt.Errorf("array: uint element overflow: %d", u)
		}
		return int64(u), nil
	case v.CanFloat():
		return v.Float(), nil
	case k == reflect.Bool:
		return v.Bool(), nil
	case k == reflect.String:
		return v.String(), nil
	case (k == reflect.Slice || (k == reflect.Array && v.CanAddr())) && v.Type().Elem().Kind() == reflect.Uint8:
		return v.Bytes(), nil
	default:
		return nil, fmt.Errorf("array: unsupported element kind %s", k)
	}
}

func makeIndexable(v reflect.Value) (reflect.Value, error) {
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		return v, nil
	case reflect.Pointer:
		if e := v.Elem(); e.Kind() == reflect.Array {
			return e, nil
		}
	}
	return reflect.Value{}, fmt.Errorf("array: bind: unsupported argument kind %s (need slice / array / *array)", v.Kind())
}

// bindings is the process-global token registry. Tokens are atomic and
// monotonically increasing; entries are inserted by [Bind] and removed by
// the release closure it returns.
var bindings = newRegistry()

type registry struct {
	next atomic.Int64
	m    sync.Map // map[int64]any
}

func newRegistry() *registry { return &registry{} }

func (r *registry) Next() int64           { return r.next.Add(1) }
func (r *registry) store(id int64, v any) { r.m.Store(id, v) }
func (r *registry) load(id int64) (any, bool) {
	v, ok := r.m.Load(id)
	return v, ok
}
func (r *registry) delete(id int64) { r.m.Delete(id) }
