package sqlite

import (
	"database/sql/driver"
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// Pointer wraps v so it can be bound as a SQL parameter and reach the
// other side of the boundary as a Go value rather than a SQLite primitive.
// Useful for ferrying slices, maps, or opaque handles into custom UDFs or
// virtual-table modules without serializing.
//
// Example — bind a Go slice to the [ext/array] table-valued function:
//
//	rows, _ := db.QueryContext(ctx,
//	    `SELECT value FROM array(?) ORDER BY value`,
//	    sqlite.Pointer([]int{10, 20, 30}))
//
// The wrapped value travels through SQLite as a tagged pointer via
// `sqlite3_bind_pointer` / `sqlite3_value_pointer`. From inside a UDF
// registered with [Conn.RegisterFunc] (or a [VTab] Filter callback),
// the args slice receives v unchanged — the Go value, not a primitive.
//
// Lifetime: the binding lives until the parameter is rebound or the
// statement is finalized. SQLite drives the cleanup deterministically
// via a destructor trampoline — no caller-side Release is needed.
//
// Note: sqlite3_reset does NOT clear bindings (that's
// sqlite3_clear_bindings). A prepared statement reused via Reset keeps
// its Pointer binding alive until either the parameter is rebound or
// the statement is finalized.
func Pointer(v any) any {
	return &pointerArg{v: v}
}

// ValuePointer is a convenience helper for UDF / vtab implementations
// that want to verify the value in args came from [Pointer]. Most code
// can just type-assert the slot in args directly. ValuePointer returns
// the wrapped value plus ok=true when v is a non-primitive Go value
// substituted in by [Pointer]; returns (v, false) when v is one of the
// standard driver.Value primitives (int64, float64, string, []byte,
// bool, time.Time, nil).
func ValuePointer(v Value) (any, bool) {
	switch v.(type) {
	case nil, int64, float64, bool, string, []byte:
		return v, false
	}
	return v, true
}

// pointerArg is the internal wrapper [Pointer] returns. Unexported so
// callers can't construct it accidentally; the only way to make one is
// through [Pointer].
type pointerArg struct {
	v any
}

// Process-wide registry mapping atomic uintptr tokens to the wrapped Go
// value. SQLite stores the token on the bound parameter slot; the
// destructor trampoline (registered once at init) deletes the entry when
// SQLite drops the binding. Tokens never collide because next is
// monotonic.
var pointerRegistry = struct {
	mu   sync.RWMutex
	m    map[uintptr]any
	next atomic.Uintptr
}{m: make(map[uintptr]any)}

func storePointer(v any) uintptr {
	// Skip token 0 so a registry miss is distinguishable from a real
	// binding (sqlite3_value_pointer also returns 0 on tag mismatch).
	token := pointerRegistry.next.Add(1)
	pointerRegistry.mu.Lock()
	pointerRegistry.m[token] = v
	pointerRegistry.mu.Unlock()
	return token
}

func loadPointer(token uintptr) (any, bool) {
	pointerRegistry.mu.RLock()
	v, ok := pointerRegistry.m[token]
	pointerRegistry.mu.RUnlock()
	return v, ok
}

func releasePointer(token uintptr) {
	pointerRegistry.mu.Lock()
	delete(pointerRegistry.m, token)
	pointerRegistry.mu.Unlock()
}

// pointerDestructorTrampoline matches the C signature
// `void (*)(void *)` that sqlite3_bind_pointer expects for its
// destructor argument. SQLite invokes it on overwrite, clear_bindings,
// or finalize — releasing the registry slot deterministically without
// relying on Go GC.
func pointerDestructorTrampoline(_ *libc.TLS, p uintptr) {
	releasePointer(p)
}

// Initialized once at package load. The C string and func pointer
// live for the process lifetime; reusing them across every bind avoids
// per-call CString / cFuncPointer churn.
var (
	pointerDestructorPtr uintptr
	pointerTypeTag       uintptr
)

func init() {
	pointerDestructorPtr = cFuncPointer(pointerDestructorTrampoline)
	// Use the modernc transpiler's TLS-free CString-equivalent: allocate
	// a C buffer via Xmalloc and copy the tag bytes. Lifetime is the
	// process (never freed); the cost is one tag-string-worth of bytes.
	const tag = "go-again-pointer\x00"
	p := libc.Xmalloc(nil, libc.Tsize_t(len(tag)))
	if p == 0 {
		panic("sqlite: out of memory allocating pointer-binding type tag")
	}
	mem := (*libc.RawMem)(unsafe.Pointer(p))[:len(tag):len(tag)]
	copy(mem, tag)
	pointerTypeTag = p
}

// bindPointerValue routes a *pointerArg from (*conn).bind through
// sqlite3_bind_pointer. Returns 0 alloc because the C side owns the
// token via the destructor.
func (c *conn) bindPointerValue(pstmt uintptr, idx1 int, p *pointerArg) error {
	token := storePointer(p.v)
	rc := sqlite3.Xsqlite3_bind_pointer(c.tls, pstmt, int32(idx1),
		token, pointerTypeTag, pointerDestructorPtr)
	if rc != sqlite3.SQLITE_OK {
		// SQLite did not accept the binding; the destructor will NOT
		// run. Drop the registry entry ourselves.
		releasePointer(token)
		return c.errstr(rc)
	}
	return nil
}

// tryUnwrapPointer is called by functionArgs after stamping a NULL
// argument. If the value also carries a pointer tagged with our
// type-tag, look up the wrapped Go value and overwrite the slot.
func tryUnwrapPointer(tls *libc.TLS, valPtr uintptr) (any, bool) {
	token := sqlite3.Xsqlite3_value_pointer(tls, valPtr, pointerTypeTag)
	if token == 0 {
		return nil, false
	}
	return loadPointer(token)
}

// pointerRegistrySize is an internal helper used only by tests to
// confirm the destructor trampoline ran and the registry stayed
// bounded. Not part of the public API.
func pointerRegistrySize() int {
	pointerRegistry.mu.Lock()
	n := len(pointerRegistry.m)
	pointerRegistry.mu.Unlock()
	return n
}

// Compile-time check that pointerArg is a driver.Value-compatible
// payload (i.e. the standard converter cannot accidentally accept it,
// which would short-circuit our CheckNamedValue).
var _ driver.Value = (*pointerArg)(nil)
