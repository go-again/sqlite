package sqlite // import "github.com/go-again/sqlite"

import (
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// The SQLite function-context substrate exposed on *FunctionContext: result and
// argument subtypes (the out-of-band tag a function attaches to a value so a
// downstream function — e.g. json_extract over a JSON-subtyped value — can act
// on it without re-parsing), plus per-argument auxiliary-data caching (a value
// computed once for a constant argument and reused across rows, e.g. a compiled
// regex). All four are pure wrappers over the corresponding sqlite3_* calls; the
// owning trampoline populates tls/ctx (and argc/argv for the arg accessors).

// auxDataReg maps a minted id to a Go value stashed via SetAuxData. The id is
// handed to SQLite as the C pAux pointer; auxDataDestroy drops it when SQLite
// evicts the entry (the next SetAuxData for the same argument, or statement
// finalize), so nothing leaks. idGen never mints 0, so id 0 means "no auxdata".
var auxDataReg = newCallbackTable[any]()

// ResultSubtype requests that this function's result value carry the given
// SQLite subtype — the value a consumer reads back via sqlite3_value_subtype
// (exposed here as [FunctionContext.ValueSubtype]). Only the low 8 bits are
// significant. It is applied by the trampoline after the result value is set,
// so it may be called anywhere in the scalar or window-function body.
//
// https://sqlite.org/c3ref/result_subtype.html
func (c *FunctionContext) ResultSubtype(subtype uint) {
	c.resultSubtype = subtype
	c.hasResultSubtype = true
}

// applyResultSubtype is invoked by the trampolines once the result value has
// been set; sqlite3_result_subtype must follow the result value, not precede it.
func (c *FunctionContext) applyResultSubtype() {
	if c.hasResultSubtype {
		sqlite3.Xsqlite3_result_subtype(c.tls, c.ctx, uint32(c.resultSubtype))
	}
}

// ValueSubtype returns the subtype of argument i (0-based) — the tag an upstream
// function attached via ResultSubtype. It returns 0 when i is out of range or
// the context carries no arguments (e.g. a window Value/Final call).
//
// https://sqlite.org/c3ref/value_subtype.html
func (c *FunctionContext) ValueSubtype(i int) uint {
	if c.argv == 0 || i < 0 || int32(i) >= c.argc {
		return 0
	}
	valPtr := *(*uintptr)(unsafe.Pointer(c.argv + uintptr(i)*sqliteValPtrSize))
	return uint(sqlite3.Xsqlite3_value_subtype(c.tls, valPtr))
}

// SetAuxData caches data against argument i so a later invocation of the same
// function for the same constant argument can retrieve it via GetAuxData — the
// idiom for compiling a pattern / parsing a path once and reusing it per row.
//
// SQLite only preserves the cache while argument i is invariant across rows;
// otherwise GetAuxData returns (nil, false) and the function must recompute. The
// cached value is released automatically when SQLite evicts it (a replacing
// SetAuxData or statement finalize) — do not hold the value past the row unless
// you own a separate reference.
//
// https://sqlite.org/c3ref/get_auxdata.html
func (c *FunctionContext) SetAuxData(i int, data any) {
	id := auxDataReg.register(data)
	sqlite3.Xsqlite3_set_auxdata(c.tls, c.ctx, int32(i), id, cFuncPointer(auxDataDestroy))
}

// GetAuxData returns the value previously cached against argument i via
// SetAuxData, or (nil, false) if none is live (never set, or evicted because
// the argument was not constant).
//
// https://sqlite.org/c3ref/get_auxdata.html
func (c *FunctionContext) GetAuxData(i int) (any, bool) {
	id := sqlite3.Xsqlite3_get_auxdata(c.tls, c.ctx, int32(i))
	if id == 0 {
		return nil, false
	}
	return auxDataReg.lookup(id)
}

// auxDataDestroy is the xDelete trampoline SQLite calls when it evicts an
// auxdata entry; p is the id we handed to set_auxdata. Dropping it from the
// registry is the whole teardown.
func auxDataDestroy(tls *libc.TLS, p uintptr) {
	auxDataReg.drop(p)
}
