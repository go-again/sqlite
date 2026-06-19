package sqlite // import "gosqlite.org"

import (
	"sync"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// BusyHandler is called when an operation cannot proceed because the database is
// locked by another connection. attempts is the number of times it has already
// been invoked for the current locking event (0 on the first call). Return true
// to wait and retry, false to give up — in which case the operation fails with
// SQLITE_BUSY.
//
// The handler runs on the connection's own goroutine while it blocks on the
// lock; a handler that returns true should sleep first (e.g. an exponential or
// jittered back-off keyed on attempts), or SQLite will spin retrying
// immediately. Returning false on the first call makes lock contention fail
// fast.
type BusyHandler func(attempts int) bool

var xBusyHandlers = struct {
	mu sync.RWMutex
	m  map[uintptr]BusyHandler
}{m: make(map[uintptr]BusyHandler)}

// RegisterBusyHandler installs handler as this connection's busy callback,
// replacing any previous handler. Pass nil to remove it.
//
// This is the programmable alternative to a fixed `busy_timeout`: use it for
// adaptive / jittered / deadline-aware retry on a high-contention WAL database.
// Note that SQLite's busy handler and busy_timeout are mutually exclusive —
// installing one clears the other.
//
// Per-connection, like the other hooks: pin the pool so the handler is on the
// connection a later query uses.
//
// https://sqlite.org/c3ref/busy_handler.html
func (c *Conn) RegisterBusyHandler(handler BusyHandler) {
	if handler == nil {
		xBusyHandlers.mu.Lock()
		delete(xBusyHandlers.m, c.db)
		xBusyHandlers.mu.Unlock()
		sqlite3.Xsqlite3_busy_handler(c.tls, c.db, 0, 0)
		return
	}
	xBusyHandlers.mu.Lock()
	xBusyHandlers.m[c.db] = handler
	xBusyHandlers.mu.Unlock()
	sqlite3.Xsqlite3_busy_handler(c.tls, c.db, cFuncPointer(busyTrampoline), c.db)
}

// busyTrampoline matches sqlite3_busy_handler's callback: int (*)(void*, int).
func busyTrampoline(tls *libc.TLS, pArg uintptr, count int32) int32 {
	xBusyHandlers.mu.RLock()
	h := xBusyHandlers.m[pArg]
	xBusyHandlers.mu.RUnlock()
	if h == nil {
		return 0
	}
	if h(int(count)) {
		return 1 // non-zero → wait and retry
	}
	return 0
}

var _ func(*libc.TLS, uintptr, int32) int32 = busyTrampoline
