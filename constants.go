package sqlite

import sqlite3 "modernc.org/sqlite/lib"

// SQLite operation codes re-exported for use by hook callbacks.
// These match the upstream SQLITE_* constants.
const (
	SQLITE_INSERT = sqlite3.SQLITE_INSERT
	SQLITE_UPDATE = sqlite3.SQLITE_UPDATE
	SQLITE_DELETE = sqlite3.SQLITE_DELETE
)

// Authorizer return codes.
const (
	SQLITE_OK     = sqlite3.SQLITE_OK
	SQLITE_DENY   = sqlite3.SQLITE_DENY
	SQLITE_IGNORE = sqlite3.SQLITE_IGNORE
)

// Authorizer action codes (a subset matching mattn).
const (
	SQLITE_CREATE_INDEX        = sqlite3.SQLITE_CREATE_INDEX
	SQLITE_CREATE_TABLE        = sqlite3.SQLITE_CREATE_TABLE
	SQLITE_CREATE_TEMP_INDEX   = sqlite3.SQLITE_CREATE_TEMP_INDEX
	SQLITE_CREATE_TEMP_TABLE   = sqlite3.SQLITE_CREATE_TEMP_TABLE
	SQLITE_CREATE_TEMP_TRIGGER = sqlite3.SQLITE_CREATE_TEMP_TRIGGER
	SQLITE_CREATE_TEMP_VIEW    = sqlite3.SQLITE_CREATE_TEMP_VIEW
	SQLITE_CREATE_TRIGGER      = sqlite3.SQLITE_CREATE_TRIGGER
	SQLITE_CREATE_VIEW         = sqlite3.SQLITE_CREATE_VIEW
	SQLITE_CREATE_VTABLE       = sqlite3.SQLITE_CREATE_VTABLE
	SQLITE_DROP_INDEX          = sqlite3.SQLITE_DROP_INDEX
	SQLITE_DROP_TABLE          = sqlite3.SQLITE_DROP_TABLE
	SQLITE_DROP_TEMP_INDEX     = sqlite3.SQLITE_DROP_TEMP_INDEX
	SQLITE_DROP_TEMP_TABLE     = sqlite3.SQLITE_DROP_TEMP_TABLE
	SQLITE_DROP_TEMP_TRIGGER   = sqlite3.SQLITE_DROP_TEMP_TRIGGER
	SQLITE_DROP_TEMP_VIEW      = sqlite3.SQLITE_DROP_TEMP_VIEW
	SQLITE_DROP_TRIGGER        = sqlite3.SQLITE_DROP_TRIGGER
	SQLITE_DROP_VIEW           = sqlite3.SQLITE_DROP_VIEW
	SQLITE_DROP_VTABLE         = sqlite3.SQLITE_DROP_VTABLE
	SQLITE_PRAGMA              = sqlite3.SQLITE_PRAGMA
	SQLITE_READ                = sqlite3.SQLITE_READ
	SQLITE_SELECT              = sqlite3.SQLITE_SELECT
	SQLITE_TRANSACTION         = sqlite3.SQLITE_TRANSACTION
	SQLITE_ATTACH              = sqlite3.SQLITE_ATTACH
	SQLITE_DETACH              = sqlite3.SQLITE_DETACH
	SQLITE_ALTER_TABLE         = sqlite3.SQLITE_ALTER_TABLE
	SQLITE_REINDEX             = sqlite3.SQLITE_REINDEX
	SQLITE_ANALYZE             = sqlite3.SQLITE_ANALYZE
	SQLITE_FUNCTION            = sqlite3.SQLITE_FUNCTION
	SQLITE_SAVEPOINT           = sqlite3.SQLITE_SAVEPOINT
	SQLITE_COPY                = sqlite3.SQLITE_COPY
	SQLITE_RECURSIVE           = sqlite3.SQLITE_RECURSIVE
)

// Result code constants (subset).
const (
	SQLITE_ERROR      = sqlite3.SQLITE_ERROR
	SQLITE_BUSY       = sqlite3.SQLITE_BUSY
	SQLITE_LOCKED     = sqlite3.SQLITE_LOCKED
	SQLITE_CONSTRAINT = sqlite3.SQLITE_CONSTRAINT
	SQLITE_MISUSE     = sqlite3.SQLITE_MISUSE
	SQLITE_NOTFOUND   = sqlite3.SQLITE_NOTFOUND
	SQLITE_FULL       = sqlite3.SQLITE_FULL
	SQLITE_DONE       = sqlite3.SQLITE_DONE
	SQLITE_ROW        = sqlite3.SQLITE_ROW
)

// Extended constraint result codes (mattn-compatible aliases).
const (
	SQLITE_CONSTRAINT_CHECK      = sqlite3.SQLITE_CONSTRAINT_CHECK
	SQLITE_CONSTRAINT_FOREIGNKEY = sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
	SQLITE_CONSTRAINT_NOTNULL    = sqlite3.SQLITE_CONSTRAINT_NOTNULL
	SQLITE_CONSTRAINT_PRIMARYKEY = sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	SQLITE_CONSTRAINT_TRIGGER    = sqlite3.SQLITE_CONSTRAINT_TRIGGER
	SQLITE_CONSTRAINT_UNIQUE     = sqlite3.SQLITE_CONSTRAINT_UNIQUE
	SQLITE_CONSTRAINT_ROWID      = sqlite3.SQLITE_CONSTRAINT_ROWID
)

// Mattn-compatible error code aliases (mattn exposes these as ErrNo values).
// Code that does `if err.Code() == sqlite.ErrConstraintUnique` continues to
// work after switching imports.
type ErrNo int

// Error makes ErrNo satisfy the error interface so it can be used as a target
// for errors.Is. Equality is the primary-code match implemented in Error.Is.
func (e ErrNo) Error() string {
	if s, ok := ErrorCodeString[int(e)]; ok {
		return s
	}
	return "sqlite errno " + itoa(int(e))
}

const (
	ErrError      ErrNo = sqlite3.SQLITE_ERROR
	ErrInternal   ErrNo = sqlite3.SQLITE_INTERNAL
	ErrPerm       ErrNo = sqlite3.SQLITE_PERM
	ErrAbort      ErrNo = sqlite3.SQLITE_ABORT
	ErrBusy       ErrNo = sqlite3.SQLITE_BUSY
	ErrLocked     ErrNo = sqlite3.SQLITE_LOCKED
	ErrNomem      ErrNo = sqlite3.SQLITE_NOMEM
	ErrReadonly   ErrNo = sqlite3.SQLITE_READONLY
	ErrInterrupt  ErrNo = sqlite3.SQLITE_INTERRUPT
	ErrIoErr      ErrNo = sqlite3.SQLITE_IOERR
	ErrCorrupt    ErrNo = sqlite3.SQLITE_CORRUPT
	ErrNotFound   ErrNo = sqlite3.SQLITE_NOTFOUND
	ErrFull       ErrNo = sqlite3.SQLITE_FULL
	ErrCantOpen   ErrNo = sqlite3.SQLITE_CANTOPEN
	ErrProtocol   ErrNo = sqlite3.SQLITE_PROTOCOL
	ErrEmpty      ErrNo = sqlite3.SQLITE_EMPTY
	ErrSchema     ErrNo = sqlite3.SQLITE_SCHEMA
	ErrTooBig     ErrNo = sqlite3.SQLITE_TOOBIG
	ErrConstraint ErrNo = sqlite3.SQLITE_CONSTRAINT
	ErrMismatch   ErrNo = sqlite3.SQLITE_MISMATCH
	ErrMisuse     ErrNo = sqlite3.SQLITE_MISUSE
	ErrNoLFS      ErrNo = sqlite3.SQLITE_NOLFS
	ErrAuth       ErrNo = sqlite3.SQLITE_AUTH
	ErrFormat     ErrNo = sqlite3.SQLITE_FORMAT
	ErrRange      ErrNo = sqlite3.SQLITE_RANGE
	ErrNotADB     ErrNo = sqlite3.SQLITE_NOTADB
	ErrNotice     ErrNo = sqlite3.SQLITE_NOTICE
	ErrWarning    ErrNo = sqlite3.SQLITE_WARNING
)

// Extended constraint error code aliases (mattn-compatible).
type ErrNoExtended int

// Error makes ErrNoExtended satisfy the error interface so it can be used as a
// target for errors.Is.
func (e ErrNoExtended) Error() string {
	if s, ok := ErrorCodeString[int(e)]; ok {
		return s
	}
	return "sqlite errno_ext " + itoa(int(e))
}

// itoa is a small dependency-free int-to-string used by ErrNo / ErrNoExtended
// Error formatting, avoiding a strconv import in this file.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

const (
	ErrConstraintCheck      ErrNoExtended = sqlite3.SQLITE_CONSTRAINT_CHECK
	ErrConstraintForeignKey ErrNoExtended = sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
	ErrConstraintNotNull    ErrNoExtended = sqlite3.SQLITE_CONSTRAINT_NOTNULL
	ErrConstraintPrimaryKey ErrNoExtended = sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	ErrConstraintTrigger    ErrNoExtended = sqlite3.SQLITE_CONSTRAINT_TRIGGER
	ErrConstraintUnique     ErrNoExtended = sqlite3.SQLITE_CONSTRAINT_UNIQUE
	ErrConstraintRowID      ErrNoExtended = sqlite3.SQLITE_CONSTRAINT_ROWID
)

// SQLITE_LIMIT_* identifiers used with Conn.GetLimit / Conn.SetLimit.
const (
	SQLITE_LIMIT_LENGTH              = sqlite3.SQLITE_LIMIT_LENGTH
	SQLITE_LIMIT_SQL_LENGTH          = sqlite3.SQLITE_LIMIT_SQL_LENGTH
	SQLITE_LIMIT_COLUMN              = sqlite3.SQLITE_LIMIT_COLUMN
	SQLITE_LIMIT_EXPR_DEPTH          = sqlite3.SQLITE_LIMIT_EXPR_DEPTH
	SQLITE_LIMIT_COMPOUND_SELECT     = sqlite3.SQLITE_LIMIT_COMPOUND_SELECT
	SQLITE_LIMIT_VDBE_OP             = sqlite3.SQLITE_LIMIT_VDBE_OP
	SQLITE_LIMIT_FUNCTION_ARG        = sqlite3.SQLITE_LIMIT_FUNCTION_ARG
	SQLITE_LIMIT_ATTACHED            = sqlite3.SQLITE_LIMIT_ATTACHED
	SQLITE_LIMIT_LIKE_PATTERN_LENGTH = sqlite3.SQLITE_LIMIT_LIKE_PATTERN_LENGTH
	SQLITE_LIMIT_VARIABLE_NUMBER     = sqlite3.SQLITE_LIMIT_VARIABLE_NUMBER
	SQLITE_LIMIT_TRIGGER_DEPTH       = sqlite3.SQLITE_LIMIT_TRIGGER_DEPTH
	SQLITE_LIMIT_WORKER_THREADS      = sqlite3.SQLITE_LIMIT_WORKER_THREADS
)
