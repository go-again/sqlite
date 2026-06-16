package sqlite

import (
	"sync"
	"time"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// UpdateHookFn is the type for an UPDATE/INSERT/DELETE notification hook
// installed via Conn.RegisterUpdateHook.
//
// op is one of sqlite.SQLITE_INSERT, SQLITE_UPDATE, or SQLITE_DELETE.
// dbName is the schema name (typically "main"). table is the affected table
// name. rowid is the rowid of the inserted/updated/deleted row.
//
// Mattn-compatibility note: mattn's callback signature uses int for op.
type UpdateHookFn = func(op int, dbName, table string, rowid int64)

// AuthorizerFn is the type for an authorizer callback installed via
// Conn.RegisterAuthorizer. The function is called for every statement
// compilation step. Return one of:
//
//   - SQLITE_OK: allow the action
//   - SQLITE_IGNORE: treat the column as NULL or skip the action silently
//   - SQLITE_DENY: reject the statement compilation with an error
//
// op is one of the SQLITE_* authorizer-action constants (see constants.go).
// arg1, arg2 are op-specific (e.g. table name, column name).
// dbName is the schema name (typically "main").
// triggerName is the name of the trigger or view causing the access; empty
// for top-level statements.
//
// Mattn-compatibility note: matches mattn's SQLiteConn.RegisterAuthorizer.
type AuthorizerFn = func(op int, arg1, arg2, dbName, triggerName string) int

// TraceEvent identifies the kind of event reported to a trace callback.
type TraceEvent uint

// Trace event mask constants. OR these together to filter which events fire.
const (
	TraceStmt    TraceEvent = sqlite3.SQLITE_TRACE_STMT
	TraceProfile TraceEvent = sqlite3.SQLITE_TRACE_PROFILE
	TraceRow     TraceEvent = sqlite3.SQLITE_TRACE_ROW
	TraceClose   TraceEvent = sqlite3.SQLITE_TRACE_CLOSE
)

// TraceInfo carries a single trace event delivered to a TraceFn.
type TraceInfo struct {
	// EventCode is the firing event: TraceStmt, TraceProfile, TraceRow, or
	// TraceClose.
	EventCode TraceEvent

	// Statement is the unexpanded SQL text passed to sqlite3_prepare* for
	// stmt/profile events. Empty for row/close events.
	Statement string

	// ExpandedSQL is the SQL with bound parameters substituted, populated for
	// stmt/profile events when the TraceConfig requested it.
	ExpandedSQL string

	// Duration is the wall-clock execution time captured for profile events.
	Duration time.Duration
}

// TraceFn is the signature of a trace callback. Return non-zero from a
// row-callback to abort the running query (SQLite contract).
type TraceFn = func(TraceInfo) int

// TraceConfig configures Conn.SetTrace.
type TraceConfig struct {
	// EventMask is an OR'd set of TraceStmt | TraceProfile | TraceRow | TraceClose.
	// Pass 0 to disable tracing entirely.
	EventMask TraceEvent

	// Callback is invoked for every matching event.
	Callback TraceFn

	// WantExpandedSQL, when true, asks sqlite to expand bound parameters into
	// the SQL text passed to the callback. Slightly more expensive.
	WantExpandedSQL bool
}

var (
	xUpdateHandlers = struct {
		mu sync.RWMutex
		m  map[uintptr]UpdateHookFn
	}{m: make(map[uintptr]UpdateHookFn)}

	xAuthorizerHandlers = struct {
		mu sync.RWMutex
		m  map[uintptr]AuthorizerFn
	}{m: make(map[uintptr]AuthorizerFn)}

	xTraceHandlers = struct {
		mu sync.RWMutex
		m  map[uintptr]*traceState
	}{m: make(map[uintptr]*traceState)}
)

type traceState struct {
	cb              TraceFn
	wantExpandedSQL bool
}

// dropHookHandlers removes every entry this conn left in the process-global
// callback registries: the per-c.db hook maps (xUpdateHandlers,
// xAuthorizerHandlers, xTraceHandlers, xPreUpdateHandlers, xCommitHandlers,
// xRollbackHandlers, xProgressHandlers, xWALHooks) and the id-keyed registries
// it minted ids in (UDF / collation / aggregate via [Conn.RegisterFunc] /
// [Conn.RegisterCollation] / [Conn.RegisterAggregator]; rtree geometry/query
// via [Conn.RegisterRTreeGeometry] / [Conn.RegisterRTreeQuery]). Called from
// (*conn).Close; without it captured closures (and their *libc.TLS) would live
// for the process lifetime, and a stale callback could fire if SQLite later
// recycled the uintptr handle for a new connection. Every new per-conn
// registry MUST be drained here.
func (c *conn) dropHookHandlers() {
	h := c.db
	xUpdateHandlers.mu.Lock()
	delete(xUpdateHandlers.m, h)
	xUpdateHandlers.mu.Unlock()
	xAuthorizerHandlers.mu.Lock()
	delete(xAuthorizerHandlers.m, h)
	xAuthorizerHandlers.mu.Unlock()
	xTraceHandlers.mu.Lock()
	delete(xTraceHandlers.m, h)
	xTraceHandlers.mu.Unlock()
	xPreUpdateHandlers.mu.Lock()
	delete(xPreUpdateHandlers.m, h)
	xPreUpdateHandlers.mu.Unlock()
	xCommitHandlers.mu.Lock()
	delete(xCommitHandlers.m, h)
	xCommitHandlers.mu.Unlock()
	xRollbackHandlers.mu.Lock()
	delete(xRollbackHandlers.m, h)
	xRollbackHandlers.mu.Unlock()
	xProgressHandlers.mu.Lock()
	delete(xProgressHandlers.m, h)
	xProgressHandlers.mu.Unlock()
	xWALHooks.mu.Lock()
	delete(xWALHooks.m, h)
	xWALHooks.mu.Unlock()
	xBusyHandlers.mu.Lock()
	delete(xBusyHandlers.m, h)
	xBusyHandlers.mu.Unlock()

	for _, id := range c.rtreeGeomIDs {
		rtreeGeom.drop(id)
	}
	c.rtreeGeomIDs = nil
	for _, id := range c.rtreeQueryIDs {
		rtreeQuery.drop(id)
	}
	c.rtreeQueryIDs = nil
	for _, id := range c.collationNeededIDs {
		collationNeeded.drop(id)
	}
	c.collationNeededIDs = nil
	for _, id := range c.ftsTokFactoryIDs {
		ftsTokFactories.drop(id)
	}
	c.ftsTokFactoryIDs = nil

	if len(c.fnIDs) > 0 {
		xFuncs.mu.Lock()
		for _, id := range c.fnIDs {
			delete(xFuncs.m, id)
			xFuncs.ids.reclaim(id)
		}
		xFuncs.mu.Unlock()
		c.fnIDs = nil
	}
	if len(c.collIDs) > 0 {
		xCollations.mu.Lock()
		for _, id := range c.collIDs {
			delete(xCollations.m, id)
			xCollations.ids.reclaim(id)
		}
		xCollations.mu.Unlock()
		c.collIDs = nil
	}
	if len(c.aggIDs) > 0 {
		xAggregateFactories.mu.Lock()
		for _, id := range c.aggIDs {
			delete(xAggregateFactories.m, id)
			xAggregateFactories.ids.reclaim(id)
		}
		xAggregateFactories.mu.Unlock()
		c.aggIDs = nil
	}
}

// RegisterUpdateHook installs a callback that fires after every INSERT, UPDATE,
// or DELETE on a rowid table. Passing a nil callback removes any previously
// installed hook.
//
// Per-connection registration. Mattn-compatibility API.
func (c *Conn) RegisterUpdateHook(fn UpdateHookFn) {
	if fn == nil {
		xUpdateHandlers.mu.Lock()
		delete(xUpdateHandlers.m, c.db)
		xUpdateHandlers.mu.Unlock()
		sqlite3.Xsqlite3_update_hook(c.tls, c.db, 0, 0)
		return
	}
	xUpdateHandlers.mu.Lock()
	xUpdateHandlers.m[c.db] = fn
	xUpdateHandlers.mu.Unlock()
	sqlite3.Xsqlite3_update_hook(c.tls, c.db, cFuncPointer(updateHookTrampoline), c.db)
}

// RegisterAuthorizer installs an authorization callback consulted for every
// access to schema objects during statement compilation. Passing a nil callback
// removes any previously installed authorizer.
//
// Per-connection registration. Mattn-compatibility API.
func (c *Conn) RegisterAuthorizer(fn AuthorizerFn) {
	if fn == nil {
		xAuthorizerHandlers.mu.Lock()
		delete(xAuthorizerHandlers.m, c.db)
		xAuthorizerHandlers.mu.Unlock()
		sqlite3.Xsqlite3_set_authorizer(c.tls, c.db, 0, 0)
		return
	}
	xAuthorizerHandlers.mu.Lock()
	xAuthorizerHandlers.m[c.db] = fn
	xAuthorizerHandlers.mu.Unlock()
	sqlite3.Xsqlite3_set_authorizer(c.tls, c.db, cFuncPointer(authorizerTrampoline), c.db)
}

// SetTrace installs (or removes, when cfg.Callback is nil or cfg.EventMask is
// zero) a tracing callback driven by sqlite3_trace_v2.
//
// Per-connection registration. Mattn-compatibility API.
func (c *Conn) SetTrace(cfg *TraceConfig) error {
	if cfg == nil || cfg.Callback == nil || cfg.EventMask == 0 {
		xTraceHandlers.mu.Lock()
		delete(xTraceHandlers.m, c.db)
		xTraceHandlers.mu.Unlock()
		sqlite3.Xsqlite3_trace_v2(c.tls, c.db, 0, 0, 0)
		return nil
	}
	xTraceHandlers.mu.Lock()
	xTraceHandlers.m[c.db] = &traceState{
		cb:              cfg.Callback,
		wantExpandedSQL: cfg.WantExpandedSQL,
	}
	xTraceHandlers.mu.Unlock()
	rc := sqlite3.Xsqlite3_trace_v2(c.tls, c.db, uint32(cfg.EventMask), cFuncPointer(traceTrampoline), c.db)
	if rc != sqlite3.SQLITE_OK {
		// Trampoline never got wired to SQLite; drop the orphan map
		// entry so the trace callback can't fire on a half-installed
		// state and so a follow-up SetTrace doesn't have to clear stale
		// state to succeed.
		xTraceHandlers.mu.Lock()
		delete(xTraceHandlers.m, c.db)
		xTraceHandlers.mu.Unlock()
		return c.errstr(rc)
	}
	return nil
}

// updateHookTrampoline matches the signature SQLite expects for sqlite3_update_hook.
func updateHookTrampoline(tls *libc.TLS, handle uintptr, op int32, zDb uintptr, zTab uintptr, rowid int64) {
	xUpdateHandlers.mu.RLock()
	fn := xUpdateHandlers.m[handle]
	xUpdateHandlers.mu.RUnlock()
	if fn == nil {
		return
	}
	fn(int(op), libc.GoString(zDb), libc.GoString(zTab), rowid)
}

// authorizerTrampoline matches the signature SQLite expects for sqlite3_set_authorizer.
func authorizerTrampoline(tls *libc.TLS, handle uintptr, op int32, zArg1, zArg2, zDB, zTrigger uintptr) int32 {
	xAuthorizerHandlers.mu.RLock()
	fn := xAuthorizerHandlers.m[handle]
	xAuthorizerHandlers.mu.RUnlock()
	if fn == nil {
		return sqlite3.SQLITE_OK
	}
	return int32(fn(
		int(op),
		libc.GoString(zArg1),
		libc.GoString(zArg2),
		libc.GoString(zDB),
		libc.GoString(zTrigger),
	))
}

// traceTrampoline matches the signature SQLite expects for sqlite3_trace_v2.
func traceTrampoline(tls *libc.TLS, mask uint32, handle, p, x uintptr) int32 {
	xTraceHandlers.mu.RLock()
	st := xTraceHandlers.m[handle]
	xTraceHandlers.mu.RUnlock()
	if st == nil {
		return 0
	}

	var info TraceInfo
	info.EventCode = TraceEvent(mask)
	switch TraceEvent(mask) {
	case TraceStmt:
		// p = sqlite3_stmt*, x = char* unexpanded SQL.
		info.Statement = libc.GoString(x)
		if st.wantExpandedSQL && p != 0 {
			ex := sqlite3.Xsqlite3_expanded_sql(tls, p)
			if ex != 0 {
				info.ExpandedSQL = libc.GoString(ex)
				sqlite3.Xsqlite3_free(tls, ex)
			}
		}
	case TraceProfile:
		// p = sqlite3_stmt*, x = int64* nanoseconds.
		if x != 0 {
			info.Duration = time.Duration(*(*int64)(unsafe.Pointer(x)))
		}
		if p != 0 {
			info.Statement = libc.GoString(sqlite3.Xsqlite3_sql(tls, p))
			if st.wantExpandedSQL {
				ex := sqlite3.Xsqlite3_expanded_sql(tls, p)
				if ex != 0 {
					info.ExpandedSQL = libc.GoString(ex)
					sqlite3.Xsqlite3_free(tls, ex)
				}
			}
		}
	case TraceRow, TraceClose:
		// No additional payload to extract.
	}
	return int32(st.cb(info))
}
