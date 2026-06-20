// compress example: keep a SQLite database compressed on disk. compress.Open
// inflates the file into a transient working copy, hands back a normal
// *sqlite.DB, and recompresses on Close — so the on-disk file is a compact
// archive that you can still open and query in place.
//
// Run from the compress module:
//
//	cd vfs/compress && go run ./example
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs/compress"
)

func main() {
	dir, err := os.MkdirTemp("", "vfs-compress-example-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "app.db.az")

	// Open: fresh (the file does not exist yet), write a lot of compressible
	// rows, then Close — which compresses the working copy over dbPath.
	db, err := compress.Open(sqlite.Config{Path: dbPath}, compress.Options{Level: compress.CompressionDefault})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		log.Fatal(err)
	}
	row := strings.Repeat("the quick brown fox jumps over the lazy dog ", 64) // ~2.8 KB, very compressible
	for i := 0; i < 2000; i++ {
		if _, err := db.Exec(`INSERT INTO notes (body) VALUES (?)`, row); err != nil {
			log.Fatal(err)
		}
	}
	if err := db.Close(); err != nil { // compresses the working copy to disk
		log.Fatal(err)
	}

	// The logical database is several MB; the on-disk file is a fraction.
	logical := int64(len(row)) * 2000
	info, _ := os.Stat(dbPath)
	fmt.Printf("on-disk compressed file: %d bytes (logical content ~%d bytes, %.0fx)\n",
		info.Size(), logical, float64(logical)/float64(info.Size()))

	// Reopen: the file is transparently inflated; query it like any database.
	db2, err := compress.Open(sqlite.Config{Path: dbPath}, compress.Options{})
	if err != nil {
		log.Fatal(err)
	}
	defer db2.Close()
	var n int
	if err := db2.QueryRow(`SELECT count(*) FROM notes`).Scan(&n); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("reopened compressed database: %d rows\n", n)
}
