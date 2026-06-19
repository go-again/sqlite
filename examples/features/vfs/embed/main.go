// vfs-embed example: bundle a SQLite database inside an embed.FS and open
// it read-only via the vfs sub-package. The DB never has to be unpacked to
// disk at runtime — useful for CLI tools that ship with seed data.
//
// To run this example you'd typically generate seed.db once at build time;
// here we synthesize the bytes via sqlite.Serialize on an in-memory DB and
// stash them in an fstest.MapFS so the rest of the flow matches the
// embed.FS case verbatim. Nothing touches the disk.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"testing/fstest"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs"
)

func main() {
	// Real apps would do:
	//   //go:embed seed.db
	//   var seedFS embed.FS
	// For a self-contained example we build the seed in memory and
	// serialize it to bytes — no disk roundtrip required.
	src, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	src.SetMaxOpenConns(1)
	if _, err := src.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, text TEXT);
INSERT INTO notes (text) VALUES ('one'), ('two'), ('three');`); err != nil {
		log.Fatal(err)
	}
	seed, err := sqlite.Serialize(context.Background(), src)
	if err != nil {
		log.Fatal(err)
	}
	src.Close()

	embed := fstest.MapFS{"seed.db": &fstest.MapFile{Data: seed}}

	// Register the in-memory FS as a SQLite VFS.
	name, fs, err := vfs.New(embed)
	if err != nil {
		log.Fatal(err)
	}
	defer fs.Close()

	// Open the database through the registered VFS in read-only mode.
	db, err := sql.Open("sqlite", "file:seed.db?vfs="+name+"&mode=ro")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT id, text FROM notes ORDER BY id")
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%d: %s\n", id, text)
	}
}
