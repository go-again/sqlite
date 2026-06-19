// gorm-vfs example: open a gorm.DB against a SQLite database that lives
// entirely inside an embed.FS-style read-only filesystem. The vfs sub-package
// handles registration; gorm sees just another DSN.
//
// Use case: CLI tools or single-binary servers that ship a seed database
// baked into the binary.
package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing/fstest"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "gosqlite.org"
	sqlitegorm "gosqlite.org/gorm"
	"gosqlite.org/vfs"
)

type Note struct {
	ID   uint
	Text string
}

func main() {
	// Real apps would use //go:embed seed.db; we build a fixture on the fly
	// so the example runs standalone.
	tmp := filepath.Join(os.TempDir(), "gorm-vfs-seed.db")
	defer os.Remove(tmp)
	src, _ := sql.Open("sqlite3", tmp)
	if _, err := src.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, text TEXT);
INSERT INTO notes (text) VALUES ('alpha'), ('beta'), ('gamma');`); err != nil {
		log.Fatal(err)
	}
	src.Close()
	seedBytes, _ := os.ReadFile(tmp)

	// Wrap the seed bytes as a Go fs.FS and register it as a SQLite VFS.
	embedded := fstest.MapFS{"seed.db": &fstest.MapFile{Data: seedBytes}}
	vfsName, _, err := vfs.New(embedded)
	if err != nil {
		log.Fatal(err)
	}

	// Open gorm against the registered VFS. mode=ro keeps writes out, since
	// the underlying fs.FS is read-only anyway.
	dsn := "file:seed.db?vfs=" + vfsName + "&mode=ro"
	db, err := gorm.Open(sqlitegorm.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatal(err)
	}

	var notes []Note
	if err := db.Order("id").Find(&notes).Error; err != nil {
		log.Fatal(err)
	}
	for _, n := range notes {
		fmt.Printf("%d: %s\n", n.ID, n.Text)
	}
}
