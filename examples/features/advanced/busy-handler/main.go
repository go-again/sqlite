// busy-handler: a programmable busy handler — adaptive/back-off retry on lock
// contention, the callback alternative to a fixed busy_timeout. Here one
// connection holds a write lock while another retries with exponential back-off
// and eventually gives up.
//
// Run with:
//
//	just example busy-handler
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	sqlite "gosqlite.org"
)

func main() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "busy")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "app.db")

	// Connection A creates the table, then holds a write lock in an open tx.
	dbA, err := sql.Open("sqlite", path)
	if err != nil {
		log.Fatal(err)
	}
	defer dbA.Close()
	if _, err := dbA.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		log.Fatal(err)
	}
	txA, err := dbA.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer txA.Rollback()
	if _, err := txA.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		log.Fatal(err) // takes a RESERVED lock A keeps until rollback
	}

	// Connection B installs a back-off busy handler, then tries to write.
	dbB, err := sql.Open("sqlite", path)
	if err != nil {
		log.Fatal(err)
	}
	defer dbB.Close()
	dbB.SetMaxOpenConns(1)
	scB, err := dbB.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer scB.Close()
	if err := scB.Raw(func(dc any) error {
		dc.(*sqlite.Conn).RegisterBusyHandler(func(attempts int) bool {
			if attempts >= 4 {
				return false // give up → SQLITE_BUSY
			}
			backoff := time.Duration(1<<attempts) * time.Millisecond
			fmt.Printf("  busy: attempt %d, backing off %v\n", attempts, backoff)
			time.Sleep(backoff)
			return true // retry
		})
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	_, err = scB.ExecContext(ctx, `INSERT INTO t VALUES (2)`)
	fmt.Printf("\n  B's write after retries: %v\n", err)
}
