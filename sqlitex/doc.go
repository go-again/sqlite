// Package sqlitex provides ergonomic helpers over a database/sql SQLite
// database: deferred savepoints, transaction wrappers, multi-statement
// scripts, a row-callback query runner, single-row and multi-row scalar reads,
// and an embed.FS schema-migration runner.
//
// The helpers operate on the standard *sql.DB / *sql.Conn / *sql.Tx types and
// work with any database/sql driver, but assume SQLite semantics (SAVEPOINT,
// BEGIN IMMEDIATE, multi-statement Exec, PRAGMA user_version). They fill the
// quality-of-life gap that users of zombiezen.com/go/sqlite and
// crawshaw.io/sqlite miss when moving to a database/sql driver — this package
// is named and shaped after their sqlitex helpers.
//
// # Deferred savepoints
//
//	release, err := sqlitex.Save(ctx, conn)
//	if err != nil { return err }
//	defer release(&err) // rolls back on a non-nil *err or panic, else releases
//
// # Transactions
//
//	err := sqlitex.Transaction(ctx, db, func(tx *sql.Tx) error {
//	    _, err := tx.ExecContext(ctx, "INSERT INTO t VALUES (?)", 1)
//	    return err // non-nil rolls back; nil commits
//	})
//
// # Row-callback queries
//
// Execute runs a query and invokes a callback per row, handling the
// Query/Next/Scan/Err/Close lifecycle. ResultStrings / ResultInts collect a
// single column across rows:
//
//	err := sqlitex.Execute(ctx, db, "SELECT name FROM users WHERE age > :min",
//	    &sqlitex.ExecOptions{
//	        Named:      map[string]any{"min": 18},
//	        ResultFunc: func(rows *sql.Rows) error { /* rows.Scan(...) */ return nil },
//	    })
//
// # Migrations
//
// Embed numbered .sql files and apply the pending ones, tracked via PRAGMA
// user_version:
//
//	//go:embed migrations/*.sql
//	var migrations embed.FS
//
//	sub, _ := fs.Sub(migrations, "migrations")
//	n, err := sqlitex.Migrate(ctx, db, sub) // n = migrations applied
package sqlitex
