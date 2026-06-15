package sqlite // import "github.com/go-again/sqlite"

import (
	"fmt"
	"sync"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// This file exposes two per-connection control knobs: a progress handler
// (a periodic callback that can interrupt long-running statements) and
// sqlite3_db_config, which reaches connection flags — notably the security
// flags DEFENSIVE / TRUSTED_SCHEMA / WRITABLE_SCHEMA — that have no PRAGMA.

// ProgressHandler is invoked roughly every N virtual-machine instructions
// during a long-running statement (see [Conn.SetProgressHandler]). Returning
// true interrupts the statement, which then fails with SQLITE_INTERRUPT.
type ProgressHandler func() bool

var xProgressHandlers = struct {
	mu sync.RWMutex
	m  map[uintptr]ProgressHandler
}{m: make(map[uintptr]ProgressHandler)}

// SetProgressHandler installs handler, called every n VM instructions while a
// statement runs — useful for progress reporting and cooperative cancellation
// (return true to interrupt). Pass n<=0 or a nil handler to remove it. This
// complements [Conn.IsInterrupted], which polls rather than calls back.
//
// https://sqlite.org/c3ref/progress_handler.html
func (c *Conn) SetProgressHandler(n int, handler ProgressHandler) {
	if handler == nil || n <= 0 {
		xProgressHandlers.mu.Lock()
		delete(xProgressHandlers.m, c.db)
		xProgressHandlers.mu.Unlock()
		sqlite3.Xsqlite3_progress_handler(c.tls, c.db, 0, 0, 0)
		return
	}
	xProgressHandlers.mu.Lock()
	xProgressHandlers.m[c.db] = handler
	xProgressHandlers.mu.Unlock()
	sqlite3.Xsqlite3_progress_handler(c.tls, c.db, int32(n), cFuncPointer(progressTrampoline), c.db)
}

// progressTrampoline matches sqlite3_progress_handler's callback:
// int (*)(void *pArg).
func progressTrampoline(tls *libc.TLS, pArg uintptr) int32 {
	xProgressHandlers.mu.RLock()
	h := xProgressHandlers.m[pArg]
	xProgressHandlers.mu.RUnlock()
	if h == nil {
		return 0
	}
	if h() {
		return 1 // non-zero interrupts the statement
	}
	return 0
}

var _ func(*libc.TLS, uintptr) int32 = progressTrampoline

// DBConfigOp selects a boolean per-connection configuration flag for
// [Conn.SetDBConfig] / [Conn.QueryDBConfig].
type DBConfigOp int32

const (
	// DBConfigDefensive disables language features that let a corrupt or
	// hostile database file harm the application (writable_schema, PRAGMA-driven
	// schema edits, …). Reachable only here, not via PRAGMA.
	DBConfigDefensive DBConfigOp = sqlite3.SQLITE_DBCONFIG_DEFENSIVE
	// DBConfigTrustedSchema controls whether the schema may invoke non-trusted
	// extension functions; off is the hardened setting.
	DBConfigTrustedSchema DBConfigOp = sqlite3.SQLITE_DBCONFIG_TRUSTED_SCHEMA
	// DBConfigWritableSchema allows direct writes to sqlite_schema.
	DBConfigWritableSchema DBConfigOp = sqlite3.SQLITE_DBCONFIG_WRITABLE_SCHEMA
	// DBConfigResetDatabase, when enabled then VACUUMed then disabled, resets a
	// database to empty.
	DBConfigResetDatabase DBConfigOp = sqlite3.SQLITE_DBCONFIG_RESET_DATABASE
	// DBConfigForeignKeys toggles foreign-key enforcement (same effect as
	// PRAGMA foreign_keys).
	DBConfigForeignKeys DBConfigOp = sqlite3.SQLITE_DBCONFIG_ENABLE_FKEY
	// DBConfigTriggers toggles trigger execution.
	DBConfigTriggers DBConfigOp = sqlite3.SQLITE_DBCONFIG_ENABLE_TRIGGER
	// DBConfigViews toggles view usage.
	DBConfigViews DBConfigOp = sqlite3.SQLITE_DBCONFIG_ENABLE_VIEW
	// DBConfigNoCkptOnClose suppresses the checkpoint that normally runs when
	// the last connection to a WAL database closes.
	DBConfigNoCkptOnClose DBConfigOp = sqlite3.SQLITE_DBCONFIG_NO_CKPT_ON_CLOSE
	// DBConfigReverseScanOrder reverses the order of unordered scans (a fuzzing
	// / robustness aid).
	DBConfigReverseScanOrder DBConfigOp = sqlite3.SQLITE_DBCONFIG_REVERSE_SCANORDER
	// DBConfigDQSDML controls the double-quoted-string-literal misfeature for
	// DML; off rejects "string" where 'string' was meant.
	DBConfigDQSDML DBConfigOp = sqlite3.SQLITE_DBCONFIG_DQS_DML
	// DBConfigDQSDDL is DBConfigDQSDML for DDL statements.
	DBConfigDQSDDL DBConfigOp = sqlite3.SQLITE_DBCONFIG_DQS_DDL
)

// SetDBConfig sets a boolean connection flag and returns its resulting value.
// These flags — especially the security ones (DEFENSIVE, TRUSTED_SCHEMA,
// WRITABLE_SCHEMA) — are reachable only through sqlite3_db_config, not PRAGMA.
//
// https://sqlite.org/c3ref/db_config.html
func (c *Conn) SetDBConfig(op DBConfigOp, enable bool) (bool, error) {
	return c.dbConfigBool(op, libc.Bool32(enable))
}

// QueryDBConfig returns the current value of a boolean connection flag without
// changing it.
func (c *Conn) QueryDBConfig(op DBConfigOp) (bool, error) {
	return c.dbConfigBool(op, -1)
}

// dbConfigBool calls sqlite3_db_config(db, op, onoff, &out). The variadic
// (int onoff, int *out) tail is passed via a va_list built in C scratch
// memory; onoff = -1 queries without changing.
func (c *Conn) dbConfigBool(op DBConfigOp, onoff int32) (bool, error) {
	pOut := c.tls.Alloc(4)
	defer c.tls.Free(4)
	*(*int32)(unsafe.Pointer(pOut)) = 0

	va := c.tls.Alloc(16) // two 8-byte va_list slots: onoff, pOut
	defer c.tls.Free(16)
	libc.VaList(va, onoff, pOut)

	rc := sqlite3.Xsqlite3_db_config(c.tls, c.db, int32(op), va)
	if rc != sqlite3.SQLITE_OK {
		return false, fmt.Errorf("sqlite: DBConfig(op=%d): %w", op, c.errstr(rc))
	}
	return *(*int32)(unsafe.Pointer(pOut)) != 0, nil
}

// CacheFlush flushes any dirty pages in this connection's page cache to disk
// without committing the open transaction, bounding the dirty-page footprint
// mid-transaction (useful in long bulk-load transactions). There is no PRAGMA
// equivalent. It is an error to call this while another connection holds a lock
// that would block the writes.
//
// https://sqlite.org/c3ref/db_cacheflush.html
func (c *Conn) CacheFlush() error {
	if rc := sqlite3.Xsqlite3_db_cacheflush(c.tls, c.db); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlite: CacheFlush: %w", c.errstr(rc))
	}
	return nil
}
