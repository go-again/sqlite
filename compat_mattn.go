// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package sqlite

// Type aliases for drop-in compatibility with github.com/mattn/go-sqlite3.
//
// Existing code that uses *sqlite3.SQLiteDriver, *sqlite3.SQLiteConn,
// sqlite3.SQLiteError etc. continues to work when the import path is changed
// from "github.com/mattn/go-sqlite3" to "github.com/go-again/sqlite".
type (
	// SQLiteDriver is an alias for Driver. Mattn compatibility.
	SQLiteDriver = Driver

	// SQLiteConn is an alias for Conn. Mattn compatibility.
	SQLiteConn = Conn

	// SQLiteStmt is an alias for Stmt. Mattn compatibility.
	SQLiteStmt = stmt

	// SQLiteRows is an alias for Rows. Mattn compatibility.
	SQLiteRows = rows

	// SQLiteResult is an alias for Result. Mattn compatibility.
	SQLiteResult = result

	// SQLiteTx is an alias for Tx. Mattn compatibility.
	SQLiteTx = tx

	// SQLiteBackup is an alias for Backup. Mattn compatibility.
	SQLiteBackup = Backup

	// SQLiteError is an alias for Error. Mattn compatibility.
	SQLiteError = Error
)

// Conn is the SQLite database connection type. It is exposed for use inside
// mattn-style ConnectHook callbacks and for low-level operations such as
// custom function registration, hooks, backup, and serialize/deserialize.
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
