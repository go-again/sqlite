package sqlite // import "gosqlite.org"

import (
	"fmt"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// This file exposes SQLite's read-only introspection and telemetry C-API as
// typed (*Conn)/(*Stmt) methods: column metadata, per-connection runtime
// counters, transaction state, and per-statement query-plan counters. All are
// thin binds over already-compiled symbols, in the same style as the rest of
// the forked wrapper.

// ColumnMetadata describes one table column, as reported by
// sqlite3_table_column_metadata. CollSeq is the declared collation sequence
// ("BINARY" when none is specified).
type ColumnMetadata struct {
	DeclType   string // declared type, e.g. "INTEGER" ("" for rowid / expressions)
	CollSeq    string // collation sequence name
	NotNull    bool   // has a NOT NULL constraint
	PrimaryKey bool   // part of the PRIMARY KEY
	AutoInc    bool   // is INTEGER PRIMARY KEY AUTOINCREMENT
}

// TableColumnMetadata returns metadata for a single column without preparing a
// query. Pass schema="" to search every attached database (main, temp, then
// attached, in that order). It wraps sqlite3_table_column_metadata; passing
// column="" with table set is the documented way to check that the table
// exists and find whether it has a rowid.
//
// https://sqlite.org/c3ref/table_column_metadata.html
func (c *Conn) TableColumnMetadata(schema, table, column string) (ColumnMetadata, error) {
	var md ColumnMetadata

	var zSchema uintptr
	if schema != "" {
		z, err := libc.CString(schema)
		if err != nil {
			return md, err
		}
		defer libc.Xfree(c.tls, z)
		zSchema = z
	}
	zTable, err := libc.CString(table)
	if err != nil {
		return md, err
	}
	defer libc.Xfree(c.tls, zTable)
	var zColumn uintptr
	if column != "" {
		z, err := libc.CString(column)
		if err != nil {
			return md, err
		}
		defer libc.Xfree(c.tls, z)
		zColumn = z
	}

	// Output slots in C scratch memory: two char** (8 bytes each) then three
	// int* (4 bytes each). The char* results point at SQLite-owned static
	// strings and must NOT be freed.
	bp := c.tls.Alloc(32)
	defer c.tls.Free(32)
	pDeclType, pCollSeq := bp, bp+8
	pNotNull, pPrimaryKey, pAutoInc := bp+16, bp+20, bp+24

	rc := sqlite3.Xsqlite3_table_column_metadata(c.tls, c.db, zSchema, zTable, zColumn,
		pDeclType, pCollSeq, pNotNull, pPrimaryKey, pAutoInc)
	if rc != sqlite3.SQLITE_OK {
		return md, fmt.Errorf("sqlite: TableColumnMetadata(%q, %q): %w", table, column, c.errstr(rc))
	}
	md.DeclType = libc.GoString(*(*uintptr)(unsafe.Pointer(pDeclType)))
	md.CollSeq = libc.GoString(*(*uintptr)(unsafe.Pointer(pCollSeq)))
	md.NotNull = *(*int32)(unsafe.Pointer(pNotNull)) != 0
	md.PrimaryKey = *(*int32)(unsafe.Pointer(pPrimaryKey)) != 0
	md.AutoInc = *(*int32)(unsafe.Pointer(pAutoInc)) != 0
	return md, nil
}

// DBStatus selects a per-connection runtime counter for [Conn.Status].
type DBStatus int32

// Per-connection status counters (sqlite3_db_status). Each returns a current
// and a high-water value; some (the CACHE_HIT/MISS/WRITE/SPILL family) only
// populate the current value.
const (
	DBStatusLookasideUsed     DBStatus = sqlite3.SQLITE_DBSTATUS_LOOKASIDE_USED
	DBStatusCacheUsed         DBStatus = sqlite3.SQLITE_DBSTATUS_CACHE_USED
	DBStatusSchemaUsed        DBStatus = sqlite3.SQLITE_DBSTATUS_SCHEMA_USED
	DBStatusStmtUsed          DBStatus = sqlite3.SQLITE_DBSTATUS_STMT_USED
	DBStatusLookasideHit      DBStatus = sqlite3.SQLITE_DBSTATUS_LOOKASIDE_HIT
	DBStatusLookasideMissSize DBStatus = sqlite3.SQLITE_DBSTATUS_LOOKASIDE_MISS_SIZE
	DBStatusLookasideMissFull DBStatus = sqlite3.SQLITE_DBSTATUS_LOOKASIDE_MISS_FULL
	DBStatusCacheHit          DBStatus = sqlite3.SQLITE_DBSTATUS_CACHE_HIT
	DBStatusCacheMiss         DBStatus = sqlite3.SQLITE_DBSTATUS_CACHE_MISS
	DBStatusCacheWrite        DBStatus = sqlite3.SQLITE_DBSTATUS_CACHE_WRITE
	DBStatusCacheSpill        DBStatus = sqlite3.SQLITE_DBSTATUS_CACHE_SPILL
	DBStatusCacheUsedShared   DBStatus = sqlite3.SQLITE_DBSTATUS_CACHE_USED_SHARED
	DBStatusDeferredFKs       DBStatus = sqlite3.SQLITE_DBSTATUS_DEFERRED_FKS
)

// Status returns a per-connection runtime counter from sqlite3_db_status: the
// current value and its high-water mark. Pass reset=true to zero the
// high-water mark after reading. This is SQLite's own cache/lookaside/memory
// accounting — distinct from [Conn.StmtCacheStats], which reports our Go-side
// prepared-statement LRU.
//
// https://sqlite.org/c3ref/db_status.html
func (c *Conn) Status(op DBStatus, reset bool) (current, highwater int, err error) {
	bp := c.tls.Alloc(8)
	defer c.tls.Free(8)
	rc := sqlite3.Xsqlite3_db_status(c.tls, c.db, int32(op), bp, bp+4, libc.Bool32(reset))
	if rc != sqlite3.SQLITE_OK {
		return 0, 0, fmt.Errorf("sqlite: Status(%d): %w", op, c.errstr(rc))
	}
	current = int(*(*int32)(unsafe.Pointer(bp)))
	highwater = int(*(*int32)(unsafe.Pointer(bp + 4)))
	return current, highwater, nil
}

// TxnState reports the transaction state of a schema (database) on this
// connection: [TxnNone], [TxnRead], or [TxnWrite]. Pass schema="" for the
// most advanced state across all attached schemas.
//
// https://sqlite.org/c3ref/txn_state.html
type TxnState int32

const (
	// TxnNone means no transaction is active on the schema.
	TxnNone TxnState = sqlite3.SQLITE_TXN_NONE
	// TxnRead means a read transaction is active (no writes yet).
	TxnRead TxnState = sqlite3.SQLITE_TXN_READ
	// TxnWrite means a write transaction is active.
	TxnWrite TxnState = sqlite3.SQLITE_TXN_WRITE
)

// String renders the transaction state as "none", "read", or "write".
func (s TxnState) String() string {
	switch s {
	case TxnNone:
		return "none"
	case TxnRead:
		return "read"
	case TxnWrite:
		return "write"
	default:
		return fmt.Sprintf("TxnState(%d)", int32(s))
	}
}

// TxnState returns the transaction state of the given schema on this
// connection. schema="" asks for the most advanced state across all schemas.
func (c *Conn) TxnState(schema string) TxnState {
	var zSchema uintptr
	if schema != "" {
		z, err := libc.CString(schema)
		if err != nil {
			return TxnNone
		}
		defer libc.Xfree(c.tls, z)
		zSchema = z
	}
	return TxnState(sqlite3.Xsqlite3_txn_state(c.tls, c.db, zSchema))
}

// Filename returns the on-disk path SQLite associates with the given schema
// ("main", "temp", or an ATTACHed name; "" means "main"). It returns "" for an
// in-memory or temporary database, and "" if the schema is unknown.
//
// https://sqlite.org/c3ref/db_filename.html
func (c *Conn) Filename(schema string) string {
	if schema == "" {
		schema = "main"
	}
	z, err := libc.CString(schema)
	if err != nil {
		return ""
	}
	defer libc.Xfree(c.tls, z)
	p := sqlite3.Xsqlite3_db_filename(c.tls, c.db, z)
	if p == 0 {
		return ""
	}
	return libc.GoString(p)
}

// AutoCommit reports whether the connection is in autocommit mode — true at rest,
// false while an explicit transaction (BEGIN, or an implicit one held open by a
// failed COMMIT) is in progress. Useful for savepoint/transaction-nesting logic.
//
// https://sqlite.org/c3ref/get_autocommit.html
func (c *Conn) AutoCommit() bool {
	return sqlite3.Xsqlite3_get_autocommit(c.tls, c.db) != 0
}

// ErrorOffset returns the byte offset into the most recently prepared SQL text
// of the token associated with the most recent error, or -1 if the error is not
// tied to a specific token. Pair it with a caret-pointed diagnostic.
//
// https://sqlite.org/c3ref/error_offset.html
func (c *Conn) ErrorOffset() int {
	return int(sqlite3.Xsqlite3_error_offset(c.tls, c.db))
}
