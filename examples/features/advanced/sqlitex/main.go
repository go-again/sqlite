// sqlitex: the ergonomic database/sql helpers — embed.FS migrations, the
// Transaction commit/rollback wrapper, deferred savepoints, and scalar reads.
//
// Run with:
//
//	just example sqlitex
package main

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"

	_ "gosqlite.org"
	"gosqlite.org/sqlitex"
)

//go:embed migrations/*.sql
var migrations embed.FS

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1) // keep the :memory: database on one connection
	defer db.Close()
	ctx := context.Background()

	// Apply the embedded NNNN_*.sql migrations, tracked via PRAGMA user_version.
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		log.Fatal(err)
	}
	n, err := sqlitex.Migrate(ctx, db, sub)
	if err != nil {
		log.Fatal(err)
	}
	uv, _ := sqlitex.ResultInt(ctx, db, `PRAGMA user_version`)
	fmt.Printf("applied %d migrations (user_version now %d)\n", n, uv)

	// Transaction: commits when the closure returns nil.
	if err := sqlitex.Transaction(ctx, db, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `INSERT INTO accounts(name, balance) VALUES ('alice', 100)`)
		return e
	}); err != nil {
		log.Fatal(err)
	}
	// ...and rolls back when it returns an error.
	_ = sqlitex.Transaction(ctx, db, func(tx *sql.Tx) error {
		_, _ = tx.ExecContext(ctx, `INSERT INTO accounts(name, balance) VALUES ('bob', 50)`)
		return errors.New("changed my mind")
	})
	count, _ := sqlitex.ResultInt(ctx, db, `SELECT count(*) FROM accounts`)
	fmt.Printf("accounts after one commit + one rollback: %d (only alice)\n", count)

	// Deferred savepoint on a pinned connection.
	conn, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	if err := topUp(ctx, conn); err != nil {
		log.Fatal(err)
	}
	bal, _ := sqlitex.ResultInt(ctx, conn, `SELECT balance FROM accounts WHERE name = 'alice'`)
	fmt.Printf("alice balance after the savepoint: %d\n", bal)

	// Execute: a row-callback query with a named parameter, then a multi-row
	// collector — no rows.Next/Scan/Err/Close boilerplate. (Run on the pinned
	// conn, since MaxOpenConns is 1 and conn holds it.)
	_, _ = conn.ExecContext(ctx, `INSERT INTO accounts(name, balance) VALUES ('carol', 200)`)
	fmt.Println("accounts with balance >= :min (sqlitex.Execute):")
	if err := sqlitex.Execute(ctx, conn, `SELECT name, balance FROM accounts WHERE balance >= :min ORDER BY name`,
		&sqlitex.ExecOptions{
			Named: map[string]any{"min": 100},
			ResultFunc: func(rows *sql.Rows) error {
				var name string
				var b int
				if err := rows.Scan(&name, &b); err != nil {
					return err
				}
				fmt.Printf("  %-6s %d\n", name, b)
				return nil
			},
		}); err != nil {
		log.Fatal(err)
	}
	names, _ := sqlitex.ResultStrings(ctx, conn, `SELECT name FROM accounts ORDER BY name`)
	fmt.Printf("all account names (sqlitex.ResultStrings): %v\n", names)
}

// topUp uses sqlitex.Save: the savepoint commits because err stays nil; had it
// returned an error, release(&err) would have rolled the update back.
func topUp(ctx context.Context, conn *sql.Conn) (err error) {
	release, err := sqlitex.Save(ctx, conn)
	if err != nil {
		return err
	}
	defer release(&err)
	_, err = conn.ExecContext(ctx, `UPDATE accounts SET balance = balance + 25 WHERE name = 'alice'`)
	return err
}
