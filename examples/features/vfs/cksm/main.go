// vfs-cksm: register a checksum VFS, build a database through it,
// corrupt a byte, and observe SQLITE_IOERR_DATA on the next read.
//
// Run with:
//
//	just example cksm
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs/cksm"
)

func main() {
	dir, err := os.MkdirTemp("", "cksm-example-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "cksm.db")

	name, fs, err := cksm.New(cksm.Options{})
	if err != nil {
		log.Fatalf("cksm.New: %v", err)
	}
	defer fs.Close()
	fmt.Printf("Registered cksm VFS as %q\n", name)

	db, err := sql.Open("sqlite", path+"?vfs="+name)
	if err != nil {
		log.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatalf("Conn: %v", err)
	}

	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return c.EnableChecksums("main")
	}); err != nil {
		log.Fatalf("EnableChecksums: %v", err)
	}
	fmt.Println("Reserved 8 trailer bytes for checksums on schema 'main'.")

	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, payload TEXT)`); err != nil {
		log.Fatalf("CREATE: %v", err)
	}
	for i := range 50 {
		if _, err := sc.ExecContext(ctx, `INSERT INTO t(payload) VALUES (?)`, fmt.Sprintf("row-%d", i)); err != nil {
			log.Fatalf("INSERT: %v", err)
		}
	}
	sc.Close()
	db.Close()

	// Tamper with a non-header byte in the first data page.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 2048); err != nil {
		log.Fatal(err)
	}
	f.Close()
	fmt.Println("Bit-rot simulated: clobbered 4 bytes at offset 2048.")

	// Reopen and observe the checksum failure.
	db2, err := sql.Open("sqlite", path+"?vfs="+name)
	if err != nil {
		log.Fatal(err)
	}
	defer db2.Close()
	rows, err := db2.Query(`SELECT * FROM t`)
	if err == nil {
		// Unexpected — the row iterator opened. Close it before
		// reporting so the example models the cleanup shape consumers
		// will copy verbatim.
		_ = rows.Close()
		fmt.Println("UNEXPECTED: read succeeded on corrupted DB")
	} else {
		fmt.Printf("Read failed as expected: %v\n", err)
	}
}
