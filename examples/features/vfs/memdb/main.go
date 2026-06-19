// vfs-memdb: open a SHARED named in-memory DB from two independent
// *sql.DB handles. Unlike vfs/mvcc, memdb has no snapshot isolation —
// a write done on one handle is visible to a reader on the other
// handle immediately, with no transaction-commit dance.
//
// Run with:
//
//	just example memdb
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	"gosqlite.org/vfs/memdb"
)

func main() {
	name, fs, err := memdb.New(memdb.Options{})
	if err != nil {
		log.Fatalf("memdb.New: %v", err)
	}
	defer fs.Close()
	fmt.Printf("Registered memdb VFS as %q\n", name)

	// SHARED DB: leading `/` in the path means every Open of this name
	// shares the same underlying page store.
	dsn := "file:/shared.db?vfs=" + name
	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer writer.Close()
	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer reader.Close()

	ctx := context.Background()
	if _, err := writer.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		log.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO t(v) VALUES ('first')`); err != nil {
		log.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `INSERT INTO t(v) VALUES ('second')`); err != nil {
		log.Fatal(err)
	}

	// Reader sees BOTH rows immediately — there is no MVCC snapshot,
	// so even a previously-open reader picks up writes the moment
	// they complete.
	var count int
	if err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&count); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("reader sees %d rows from writer (no snapshot isolation)\n", count)

	// PRIVATE DB: no leading slash → each Open gets its own isolated
	// store, useful for parallel test cases that want a shared VFS
	// without shared state.
	p1, _ := sql.Open("sqlite", "file:scratch?vfs="+name)
	defer p1.Close()
	p2, _ := sql.Open("sqlite", "file:scratch?vfs="+name)
	defer p2.Close()
	if _, err := p1.ExecContext(ctx, `CREATE TABLE z(v INT)`); err != nil {
		log.Fatal(err)
	}
	if _, err := p1.ExecContext(ctx, `INSERT INTO z VALUES (42)`); err != nil {
		log.Fatal(err)
	}
	// p2 must NOT see p1's table — same name, but private store.
	if _, err := p2.QueryContext(ctx, `SELECT * FROM z`); err == nil {
		fmt.Println("UNEXPECTED: private DBs leaked across opens")
	} else {
		fmt.Println("private DBs are isolated: p2 sees no table from p1")
	}
}
