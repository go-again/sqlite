package sqlite

import sqlite3 "modernc.org/sqlite/lib"

// GetLimit returns the current value of one of the SQLite per-connection limits
// (SQLITE_LIMIT_*). It does not modify the limit.
//
// Mattn-compatibility API equivalent to SQLiteConn.GetLimit.
func (c *Conn) GetLimit(id int) int {
	return int(sqlite3.Xsqlite3_limit(c.tls, c.db, int32(id), -1))
}

// SetLimit sets the given SQLITE_LIMIT_* identifier to newVal and returns the
// previous value. Pass -1 for newVal to leave the limit unchanged (use
// GetLimit if that's all you want).
//
// Mattn-compatibility API equivalent to SQLiteConn.SetLimit.
func (c *Conn) SetLimit(id, newVal int) int {
	return int(sqlite3.Xsqlite3_limit(c.tls, c.db, int32(id), int32(newVal)))
}
