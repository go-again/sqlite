// config: opens a SQLite database via the modern Go-typed
// [sqlite.Config] API — no DSN string assembly, no `_pragma=…` URL
// flags. The returned *sqlite.DB embeds *sql.DB, so every database/sql
// method works unchanged, and a single defer db.Close() releases the
// connection pool.
//
// For an encrypted database with the same Config shape, see the
// gosqlite.org/vfs/crypto module's example (crypto.Open).
//
// Run with:
//
//	just example config
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	sqlite "gosqlite.org"
)

func main() {
	dir, _ := os.MkdirTemp("", "sqlite-config-*")
	defer os.RemoveAll(dir)

	demoPlain(filepath.Join(dir, "plain.db"))
}

// demoPlain shows the simplest modern open: just Path + the
// recommended Pragmas preset. No DSN, no fmt.Sprintf, no manual
// `?vfs=…&_pragma=…` flags.
func demoPlain(dbPath string) {
	fmt.Println("=== Plain database via sqlite.Config ===")

	db, err := sqlite.Open(sqlite.Config{
		Path:         dbPath,
		Pragmas:      sqlite.RecommendedPragmas(), // WAL + busy_timeout=5s + foreign_keys
		MaxOpenConns: 4,
	})
	if err != nil {
		log.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()

	// db embeds *sql.DB — every database/sql method works.
	if _, err := db.Exec(`
		CREATE TABLE notes (
			id   INTEGER PRIMARY KEY,
			body TEXT NOT NULL
		);
		INSERT INTO notes (body) VALUES ('hello from config'), ('round-trip works');
	`); err != nil {
		log.Fatalf("CREATE/INSERT: %v", err)
	}

	rows, _ := db.Query(`SELECT id, body FROM notes ORDER BY id`)
	defer rows.Close()
	for rows.Next() {
		var id int
		var body string
		_ = rows.Scan(&id, &body)
		fmt.Printf("  id=%d  body=%q\n", id, body)
	}

	// Confirm the Pragmas actually applied.
	var mode string
	db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	fmt.Printf("journal_mode=%s (Config.Pragmas.JournalMode applied)\n", mode)
}
