package sqlite

// Type aliases for drop-in compatibility with the cgo-based
// github.com/mattn/go-sqlite3 driver.
//
// Existing code that uses *sqlite3.SQLiteDriver, *sqlite3.SQLiteConn,
// sqlite3.SQLiteError etc. continues to work when the import path is changed
// to "gosqlite.org".

// Conn is the SQLite database connection type. It is exposed for use inside
// ConnectHook callbacks and for low-level operations such as custom function
// registration, hooks, backup, and serialize/deserialize.
//
// Conn is identical to the underlying connection struct used by the driver;
// the alias gives the type an exported name without renaming the internal
// receiver across every method definition.
type Conn = conn

// Stmt is the SQLite prepared statement type, exposed for advanced use.
type Stmt = stmt

// Rows is the SQLite result set type, exposed for advanced use.
type Rows = rows

// Result is the SQLite Exec result, exposed for advanced use.
type Result = result

// Tx is the SQLite transaction type, exposed for advanced use.
type Tx = tx

type (
	// SQLiteDriver is an alias for Driver.
	SQLiteDriver = Driver

	// SQLiteConn is an alias for Conn.
	SQLiteConn = Conn

	// SQLiteStmt is an alias for Stmt.
	SQLiteStmt = Stmt

	// SQLiteRows is an alias for Rows.
	SQLiteRows = Rows

	// SQLiteResult is an alias for Result.
	SQLiteResult = Result

	// SQLiteTx is an alias for Tx.
	SQLiteTx = Tx

	// SQLiteBackup is an alias for Backup.
	SQLiteBackup = Backup

	// SQLiteError is an alias for Error.
	SQLiteError = Error

	// SQLiteContext is an alias for FunctionContext. mattn-go-sqlite3
	// uses *SQLiteContext as the receiver for user-defined function
	// callbacks; we use *FunctionContext for the same role. The alias
	// lets mattn UDF code compile after an import-path swap without
	// renaming every callback signature.
	SQLiteContext = FunctionContext
)
