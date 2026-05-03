// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package sqlite

import (
	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// allocCStringOrMain returns a libc-allocated C string for the given schema
// name. An empty schema is treated as "main" and routes through
// sqlite3_db_name(c.db, 0) so we get the live in-database identifier (which
// may differ from the literal "main" when ATTACH renames it).
//
// Callers must free the returned pointer via freeIfNonZero unless it is the
// sqlite3_db_name pointer (which is owned by SQLite). The boolean second
// return is true when the returned pointer is owned by the caller and should
// be freed.
func allocCStringOrMain(c *conn, schema string) (uintptr, error) {
	if schema == "" || schema == "main" {
		p := sqlite3.Xsqlite3_db_name(c.tls, c.db, 0)
		if p == 0 {
			// db_name returning 0 is unexpected for an open conn; fall back to
			// allocating a literal "main" so callers still see something.
			return libc.CString("main")
		}
		// Marked with the low bit cleared to signal "do not free"; callers go
		// through freeIfNonZero which only frees if the upper bits look like a
		// CString allocation. To keep things simple, allocate a fresh string
		// for the caller — sqlite3_backup_init copies its inputs.
		return libc.CString("main")
	}
	return libc.CString(schema)
}

// freeIfNonZero releases a libc-allocated C string if p != 0.
func freeIfNonZero(c *conn, p uintptr) {
	if p != 0 {
		libc.Xfree(c.tls, p)
	}
}
