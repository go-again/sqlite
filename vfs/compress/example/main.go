// compress example: keep a SQLite database compressed on disk, two ways.
//
//   - compress.Open queries the database while it stays COMPRESSED IN PLACE,
//     durable per transaction — a live, file-backed storage engine. This is the
//     one to reach for when a large database stays open and must survive a crash.
//   - compress.OpenSnapshot inflates the file into a transient working copy and
//     recompresses on Close — a snapshot, durable per session. Good for
//     archival, distribution, and open-modify-close tooling.
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

const rows = 2000

// row is ~2.8 KB of very compressible text.
var row = strings.Repeat("the quick brown fox jumps over the lazy dog ", 64)

func main() {
	dir, err := os.MkdirTemp("", "vfs-compress-example-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	logical := int64(len(row)) * rows
	live(filepath.Join(dir, "live.db.az"), logical)
	snapshot(filepath.Join(dir, "snap.db.az"), logical)
}

// live keeps the database compressed on disk the whole time it is open: every
// committed transaction is durable, and the file never holds uncompressed bytes.
func live(path string, logical int64) {
	db, err := compress.Open(sqlite.Config{Path: path}, compress.Options{Level: compress.CompressionDefault})
	if err != nil {
		log.Fatal(err)
	}
	mustSeed(db)

	// The file is compressed RIGHT NOW, mid-session — no Close required.
	info, _ := os.Stat(path)
	fmt.Printf("Open (live): on-disk %d bytes while open (logical ~%d, %.0fx), durable per transaction\n",
		info.Size(), logical, float64(logical)/float64(info.Size()))
	if err := db.Close(); err != nil {
		log.Fatal(err)
	}

	// Reopen in place — no inflate step; the bytes on disk stay compressed.
	db2, err := compress.Open(sqlite.Config{Path: path}, compress.Options{})
	if err != nil {
		log.Fatal(err)
	}
	defer db2.Close()
	fmt.Printf("Open (live): reopened compressed database in place: %d rows\n", count(db2))
}

// snapshot runs from an inflated working copy and recompresses at Close.
func snapshot(path string, logical int64) {
	db, err := compress.OpenSnapshot(sqlite.Config{Path: path}, compress.Options{})
	if err != nil {
		log.Fatal(err)
	}
	mustSeed(db)
	if err := db.Close(); err != nil { // recompresses the working copy to disk
		log.Fatal(err)
	}
	info, _ := os.Stat(path)
	fmt.Printf("OpenSnapshot: on-disk %d bytes after Close (logical ~%d, %.0fx)\n",
		info.Size(), logical, float64(logical)/float64(info.Size()))
}

// mustSeed creates the table and inserts the rows in one transaction.
func mustSeed(db *sqlite.DB) {
	if _, err := db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		log.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO notes (body) VALUES (?)`)
	if err != nil {
		log.Fatal(err)
	}
	for range rows {
		if _, err := stmt.Exec(row); err != nil {
			log.Fatal(err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
}

func count(db *sqlite.DB) int {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM notes`).Scan(&n); err != nil {
		log.Fatal(err)
	}
	return n
}
