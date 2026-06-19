// ext-blobio: stream the contents of a pre-sized BLOB column via
// readblob / writeblob SQL functions. Demonstrates the recommended
// pattern: INSERT a zeroblob(N) row, then write into it incrementally
// without ever loading the full BLOB into a SQL string.
//
// Run with:
//
//	just example blobio
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	sqlite "gosqlite.org"

	// Blank-import auto-registers readblob and writeblob on every
	// connection.
	_ "gosqlite.org/ext/blobio/auto"
)

func main() {
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

	if _, err := sc.ExecContext(ctx, `CREATE TABLE pages(id INTEGER PRIMARY KEY, body BLOB)`); err != nil {
		log.Fatalf("CREATE: %v", err)
	}

	const pageSize = 64
	res, err := sc.ExecContext(ctx, `INSERT INTO pages(body) VALUES (zeroblob(?))`, pageSize)
	if err != nil {
		log.Fatalf("INSERT zeroblob: %v", err)
	}
	id, _ := res.LastInsertId()
	fmt.Printf("Allocated %d-byte zeroblob at rowid=%d\n", pageSize, id)

	// Write a header at offset 0 and a footer at offset 56.
	if _, err := sc.ExecContext(ctx,
		`SELECT writeblob('main', 'pages', 'body', ?, 0, ?)`, id, []byte("PAGE-HEADER-v1")); err != nil {
		log.Fatalf("writeblob header: %v", err)
	}
	if _, err := sc.ExecContext(ctx,
		`SELECT writeblob('main', 'pages', 'body', ?, ?, ?)`, id, pageSize-len("EOF"), []byte("EOF")); err != nil {
		log.Fatalf("writeblob footer: %v", err)
	}

	// Read selected slices back.
	var head []byte
	if err := sc.QueryRowContext(ctx,
		`SELECT readblob('main', 'pages', 'body', ?, 0, 14)`, id).Scan(&head); err != nil {
		log.Fatalf("readblob head: %v", err)
	}
	fmt.Printf("head[0:14] = %q\n", head)

	var foot []byte
	if err := sc.QueryRowContext(ctx,
		`SELECT readblob('main', 'pages', 'body', ?, ?, 3)`, id, pageSize-3).Scan(&foot); err != nil {
		log.Fatalf("readblob foot: %v", err)
	}
	fmt.Printf("foot[61:64] = %q\n", foot)

	// Confirm the unused middle is still zero.
	var mid []byte
	if err := sc.QueryRowContext(ctx,
		`SELECT readblob('main', 'pages', 'body', ?, 14, 8)`, id).Scan(&mid); err != nil {
		log.Fatalf("readblob mid: %v", err)
	}
	allZero := true
	for _, b := range mid {
		if b != 0 {
			allZero = false
			break
		}
	}
	fmt.Printf("mid[14:22] all-zero = %v\n", allZero)

	// Use the typed Conn.OpenBlob API for the same data from Go.
	var c *sqlite.Conn
	_ = sc.Raw(func(driverConn any) error { c = driverConn.(*sqlite.Conn); return nil })
	b, err := c.OpenBlob("main", "pages", "body", id, false)
	if err != nil {
		log.Fatalf("OpenBlob: %v", err)
	}
	defer b.Close()
	fmt.Printf("Conn.OpenBlob: size=%d\n", b.Size())

	_ = sql.ErrNoRows
}
