// vfs-mvcc: open a SHARED named in-memory MVCC DB from two
// independent *sql.DB handles and observe snapshot semantics — a
// reader sees its captured view through the lifetime of its
// transaction, even while a writer commits new rows in between.
//
// Run with:
//
//	just example vfs-mvcc
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs/mvcc"
)

func main() {
	name, fs, err := mvcc.New(mvcc.Options{})
	if err != nil {
		log.Fatalf("mvcc.New: %v", err)
	}
	defer fs.Close()
	fmt.Printf("Registered MVCC VFS as %q\n", name)

	// SHARED DB: leading `/` in the path means every Open of this name
	// sees the same underlying page store.
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

	// Reader sees the row via the shared store.
	var got string
	if err := reader.QueryRowContext(ctx, `SELECT v FROM t WHERE id=1`).Scan(&got); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("reader sees row from writer: %q\n", got)

	// PRIVATE DB: no leading slash → each Open gets its own isolated
	// store, suitable for parallel test cases that want to share the
	// VFS without sharing state.
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
