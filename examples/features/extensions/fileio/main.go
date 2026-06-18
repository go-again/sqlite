// ext-fileio: read a sandboxed fs.FS via readfile + fsdir, and (with
// the os-backed variant) walk a real directory.
//
// Run with:
//
//	just example fileio
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"testing/fstest"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/fileio"
)

func main() {
	// Sandboxed example: register fileio against a synthetic fstest.MapFS
	// so untrusted SQL has no access to the real filesystem. writefile
	// is intentionally not registered in this mode.
	fsys := fstest.MapFS{
		"docs/intro.md": &fstest.MapFile{Data: []byte("# Hello\nFirst doc.\n")},
		"docs/howto.md": &fstest.MapFile{Data: []byte("# Hello\nHow-to.\n")},
		"data/cfg.json": &fstest.MapFile{Data: []byte(`{"k":"v"}`)},
	}

	db, err := sqlite.Open(sqlite.Config{Path: ":memory:"})
	if err != nil {
		log.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatalf("Conn: %v", err)
	}
	defer sc.Close()

	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return fileio.RegisterFS(c, fsys)
	}); err != nil {
		log.Fatalf("RegisterFS: %v", err)
	}

	// Walk the sandbox via the fsdir vtab.
	rows, err := sc.QueryContext(ctx,
		`SELECT name, level FROM fsdir(?) WHERE name != '' ORDER BY name`, ".")
	if err != nil {
		log.Fatalf("fsdir: %v", err)
	}
	defer rows.Close()
	fmt.Println("Walk of sandboxed FS:")
	fmt.Println("level | name")
	fmt.Println("------+------------------")
	for rows.Next() {
		var name string
		var level int
		if err := rows.Scan(&name, &level); err != nil {
			log.Fatalf("Scan: %v", err)
		}
		fmt.Printf("%-5d | %s\n", level, name)
	}

	// Read one of the files back through readfile().
	var doc []byte
	if err := sc.QueryRowContext(ctx,
		`SELECT readfile(?)`, "docs/intro.md").Scan(&doc); err != nil {
		log.Fatalf("readfile: %v", err)
	}
	fmt.Println()
	fmt.Printf("readfile('docs/intro.md'):\n%s\n", doc)

	// Confirm writefile is genuinely unavailable in sandboxed mode.
	_, err = sc.ExecContext(ctx, `SELECT writefile('/tmp/x', 'data')`)
	if err == nil {
		fmt.Println()
		fmt.Println("UNEXPECTED: writefile succeeded in sandbox mode")
	} else {
		fmt.Println()
		fmt.Printf("writefile is sandboxed: %v\n", err)
	}

	_ = sql.ErrNoRows
}
