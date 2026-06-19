// hooks: install update / authorizer / commit / trace hooks on a
// single pinned connection and observe them firing as the connection
// runs DDL + DML.
//
// Pinning is the load-bearing step: hooks are per-connection but the
// database/sql pool can hand you a different physical conn for every
// operation. The MaxOpenConns(1) + db.Conn(ctx) + sc.Raw idiom below
// is the only safe way to install a hook and then drive traffic on
// the same conn it was installed on.
//
// Run with:
//
//	just example hooks
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	sqlite "gosqlite.org"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// CRITICAL: pin to a single physical connection. Without this
	// the pool can hand subsequent ExecContext calls a different conn
	// than the one we installed the hooks on, and the hooks never fire.
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer sc.Close()

	var updates, commits, traced int
	if err := sc.Raw(func(driverConn any) error {
		c := driverConn.(*sqlite.Conn)

		c.RegisterUpdateHook(func(op int, dbName, table string, rowid int64) {
			updates++
			fmt.Printf("[update] op=%d db=%s table=%s rowid=%d\n", op, dbName, table, rowid)
		})
		c.RegisterCommitHook(func() int32 {
			commits++
			return 0
		})
		c.RegisterAuthorizer(func(op int, arg1, arg2, dbName, trigger string) int {
			// Allow everything; an authorizer that returned SQLITE_DENY
			// would block the operation at compile time.
			return sqlite.SQLITE_OK
		})
		return c.SetTrace(&sqlite.TraceConfig{
			EventMask: sqlite.TraceStmt,
			Callback: func(info sqlite.TraceInfo) int {
				traced++
				return 0
			},
		})
	}); err != nil {
		log.Fatalf("install hooks: %v", err)
	}

	for _, q := range []string{
		`CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`,
		`INSERT INTO t(v) VALUES ('one')`,
		`INSERT INTO t(v) VALUES ('two')`,
		`UPDATE t SET v='ONE' WHERE id=1`,
		`DELETE FROM t WHERE id=2`,
	} {
		if _, err := sc.ExecContext(ctx, q); err != nil {
			log.Fatalf("exec %q: %v", q, err)
		}
	}

	fmt.Printf("updates=%d commits=%d traced=%d\n", updates, commits, traced)
}
