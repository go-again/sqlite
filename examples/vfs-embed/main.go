// vfs-embed example: bundle a SQLite database inside an embed.FS and open
// it read-only via the vfs sub-package. The DB never has to be unpacked to
// disk at runtime — useful for CLI tools that ship with seed data.
//
// To run this example you'd typically generate seed.db once at build time;
// here we stub it out with a fixture created on the fly via a temp file +
// os.ReadFile so the example compiles standalone.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing/fstest"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs"
)

func main() {
	// Real apps would do:
	//   //go:embed seed.db
	//   var seedFS embed.FS
	// For a self-contained example we generate the seed at runtime and put
	// it into a fstest.MapFS so the rest of the flow matches the embed.FS
	// case verbatim.
	tmp := filepath.Join(os.TempDir(), "vfs-embed-seed.db")
	defer os.Remove(tmp)
	src, _ := sql.Open("sqlite3", tmp)
	if _, err := src.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, text TEXT);
INSERT INTO notes (text) VALUES ('one'), ('two'), ('three');`); err != nil {
		log.Fatal(err)
	}
	src.Close()
	seed, _ := os.ReadFile(tmp)
	embed := fstest.MapFS{"seed.db": &fstest.MapFile{Data: seed}}

	// Register the in-memory FS as a SQLite VFS.
	name, _, err := vfs.New(embed)
	if err != nil {
		log.Fatal(err)
	}

	// Open the database through the registered VFS in read-only mode.
	db, err := sql.Open("sqlite3", "file:seed.db?vfs="+name+"&mode=ro")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rows, _ := db.Query("SELECT id, text FROM notes ORDER BY id")
	defer rows.Close()
	for rows.Next() {
		var id int
		var text string
		rows.Scan(&id, &text)
		fmt.Printf("%d: %s\n", id, text)
	}
}
