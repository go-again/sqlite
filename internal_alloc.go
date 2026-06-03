package sqlite

import (
	"modernc.org/libc"
)

// allocCStringOrMain returns a libc-allocated C string for the given
// schema name. An empty schema collapses to the literal "main".
//
// Callers must free the returned pointer via [freeIfNonZero].
//
// (Earlier versions of this helper consulted sqlite3_db_name to honour
// an ATTACH-renamed main, but every caller passes the result to
// sqlite3_backup_init or sqlite3_file_control, both of which copy the
// input string — and "main" is itself the canonical name regardless of
// ATTACH alias. The lookup was dead weight; the explicit literal
// avoids surprising the reader.)
func allocCStringOrMain(c *conn, schema string) (uintptr, error) {
	_ = c
	if schema == "" {
		schema = "main"
	}
	return libc.CString(schema)
}

// freeIfNonZero releases a libc-allocated C string if p != 0.
func freeIfNonZero(c *conn, p uintptr) {
	if p != 0 {
		libc.Xfree(c.tls, p)
	}
}
