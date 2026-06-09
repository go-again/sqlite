package sqlite

import (
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// EnableLoadExtension toggles the sqlite3_load_extension C API on or off for
// this connection. SQLite requires this to be enabled before LoadExtension can
// be called; it is disabled by default for security.
//
// Mattn compatibility: equivalent to enabling the extension flag before
// calling LoadExtension.
func (c *Conn) EnableLoadExtension(on bool) error {
	return c.enableLoadExtension(on)
}

func (c *conn) enableLoadExtension(on bool) error {
	v := int32(0)
	if on {
		v = 1
	}
	if rc := sqlite3.Xsqlite3_enable_load_extension(c.tls, c.db, v); rc != sqlite3.SQLITE_OK {
		return c.errstr(rc)
	}
	return nil
}

// LoadExtension loads the SQLite extension at libPath into this connection,
// optionally calling the named entry point (pass "" for the default). The
// extension flag must be enabled first via EnableLoadExtension(true) unless
// the connection was opened by a Driver whose Extensions field was non-empty.
//
// Mattn compatibility: equivalent to mattn's SQLiteConn.LoadExtension.
//
// Platform note: success depends on dynamic-loader support in
// modernc.org/libc. As of the libc release pinned in `go.mod`, libc's
// Xdlopen (darwin) and XLoadLibraryW (windows) shims are not
// implemented and abort the process with "TODOTODO" the moment they
// are reached — which happens even on the disabled-extensions error
// path. linux and freebsd are fine. The test suite skips both
// LoadExtension tests on darwin and windows for that reason; the
// positive path is platform-dependent.
func (c *Conn) LoadExtension(libPath, entry string) error {
	return c.loadExtension(libPath, entry)
}

func (c *conn) loadExtension(libPath, entry string) error {
	cLib, err := libc.CString(libPath)
	if err != nil {
		return err
	}
	defer libc.Xfree(c.tls, cLib)

	var cEntry uintptr
	if entry != "" {
		cEntry, err = libc.CString(entry)
		if err != nil {
			return err
		}
		defer libc.Xfree(c.tls, cEntry)
	}

	// The output error-message slot must live in C memory, not on the Go
	// stack — a Go stack address passed as uintptr is untracked and a stack
	// move would leave sqlite3_load_extension writing through a stale
	// pointer (same class of bug as conn.prepare's ppStmt, fixed in OpenBlob).
	ppErrMsg, err := c.malloc(int(ptrSize))
	if err != nil {
		return err
	}
	defer c.free(ppErrMsg)

	rc := sqlite3.Xsqlite3_load_extension(c.tls, c.db, cLib, cEntry, ppErrMsg)
	if rc != sqlite3.SQLITE_OK {
		pErrMsg := *(*uintptr)(unsafe.Pointer(ppErrMsg))
		msg := ""
		if pErrMsg != 0 {
			msg = libc.GoString(pErrMsg)
			sqlite3.Xsqlite3_free(c.tls, pErrMsg)
		}
		if msg != "" {
			return &Error{msg: msg, code: int(rc)}
		}
		return c.errstr(rc)
	}
	return nil
}
