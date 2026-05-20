// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

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
// Platform note: success depends on dlopen support in modernc.org/libc.
// As of v1.72.x, libc's Xdlopen is not implemented on darwin — calling
// LoadExtension there with a valid extension path will trigger libc's
// "TODOTODO" abort path even if EnableLoadExtension(true) was called. On
// linux/freebsd/windows the call works as expected. Plan-audit-followup.md
// P2.12 documents the constraint and the rationale for leaving the negative
// path the only one we currently exercise in tests.
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

	var pErrMsg uintptr
	rc := sqlite3.Xsqlite3_load_extension(c.tls, c.db, cLib, cEntry, uintptr(unsafe.Pointer(&pErrMsg)))
	if rc != sqlite3.SQLITE_OK {
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
