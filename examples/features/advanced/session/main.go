// session: capture every change to one database as a changeset and replay it
// on another — the foundation for offline sync, audit logs, and lightweight
// replication. Then undo it by applying the inverse. Pure Go, no CGo; this is
// the SQLite SESSION extension exposed as a typed Go API.
//
// Run with:
//
//	just example session
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	sqlite "gosqlite.org"
)

const schema = `CREATE TABLE accounts(id INTEGER PRIMARY KEY, owner TEXT, balance INTEGER)`

func main() {
	ctx := context.Background()
	primary := open(ctx)
	defer primary.db.Close()
	replica := open(ctx)
	defer replica.db.Close()

	mustExec(ctx, primary.sc, schema)
	mustExec(ctx, replica.sc, schema)

	// Record every change made to the primary into a changeset.
	var changeset []byte
	primary.session(func(c *sqlite.Conn, sess *sqlite.Session) {
		drv(ctx, c, `INSERT INTO accounts VALUES (1, 'alice', 100), (2, 'bob', 50)`)
		drv(ctx, c, `UPDATE accounts SET balance = balance - 30 WHERE id = 1`)
		drv(ctx, c, `UPDATE accounts SET balance = balance + 30 WHERE id = 2`)
		var err error
		if changeset, err = sess.Changeset(); err != nil {
			log.Fatalf("changeset: %v", err)
		}
	})
	fmt.Printf("captured a %d-byte changeset from the primary\n", len(changeset))

	// Replay it onto the (empty) replica.
	replica.raw(func(c *sqlite.Conn) {
		if err := c.ApplyChangeset(changeset); err != nil {
			log.Fatalf("apply: %v", err)
		}
	})
	fmt.Println("\nreplica after replay:")
	dump(ctx, replica.sc)

	// Undo by applying the inverse — INSERTs become DELETEs, UPDATEs reverse.
	replica.raw(func(c *sqlite.Conn) {
		inverse, err := c.InvertChangeset(changeset)
		if err != nil {
			log.Fatalf("invert: %v", err)
		}
		if err := c.ApplyChangeset(inverse); err != nil {
			log.Fatalf("apply inverse: %v", err)
		}
	})
	fmt.Println("\nreplica after applying the inverse (rolled back):")
	dump(ctx, replica.sc)
}

// pinned bundles a *sql.DB pinned to a single connection with that conn.
type pinned struct {
	db *sql.DB
	sc *sql.Conn
}

func open(ctx context.Context) *pinned {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	return &pinned{db: db, sc: sc}
}

// raw runs fn with the driver *sqlite.Conn behind the pinned connection.
func (p *pinned) raw(fn func(*sqlite.Conn)) {
	if err := p.sc.Raw(func(dc any) error {
		fn(dc.(*sqlite.Conn))
		return nil
	}); err != nil {
		log.Fatal(err)
	}
}

// session opens a change-recording session on every table, runs fn with it,
// and closes it afterward.
func (p *pinned) session(fn func(*sqlite.Conn, *sqlite.Session)) {
	p.raw(func(c *sqlite.Conn) {
		sess, err := c.CreateSession("main")
		if err != nil {
			log.Fatalf("create session: %v", err)
		}
		defer sess.Close()
		if err := sess.Attach(""); err != nil {
			log.Fatalf("attach: %v", err)
		}
		fn(c, sess)
	})
}

// drv runs a parameterless statement on the driver connection, so it lands on
// the same conn the session is recording.
func drv(ctx context.Context, c *sqlite.Conn, query string) {
	if _, err := c.ExecContext(ctx, query, nil); err != nil {
		log.Fatalf("exec %q: %v", query, err)
	}
}

// mustExec runs query on the pinned connection (db.ExecContext would deadlock
// against the checked-out sc when MaxOpenConns is 1).
func mustExec(ctx context.Context, sc *sql.Conn, query string) {
	if _, err := sc.ExecContext(ctx, query); err != nil {
		log.Fatalf("exec %q: %v", query, err)
	}
}

func dump(ctx context.Context, sc *sql.Conn) {
	rows, err := sc.QueryContext(ctx, `SELECT id, owner, balance FROM accounts ORDER BY id`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, balance int
		var owner string
		if err := rows.Scan(&id, &owner, &balance); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %d %-6s %d\n", id, owner, balance)
		n++
	}
	if n == 0 {
		fmt.Println("  (no rows)")
	}
}
