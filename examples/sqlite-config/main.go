// sqlite-config: opens a SQLite database via the modern Go-typed
// [sqlite.Config] API — no DSN string assembly, no `_pragma=…` URL
// flags. Shows two flavors:
//
//  1. A plain database with the recommended production Pragmas.
//  2. An encrypted database using the same Config shape — only one
//     extra field set.
//
// The returned *sqlite.DB embeds *sql.DB, so every database/sql
// method works unchanged. A single defer db.Close() releases the
// connection pool AND any encryption VFS the open registered.
//
// Run with:
//
//	just example sqlite-config
package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs/crypto"
)

func main() {
	dir, _ := os.MkdirTemp("", "sqlite-config-*")
	defer os.RemoveAll(dir)

	demoPlain(filepath.Join(dir, "plain.db"))
	fmt.Println()
	demoEncrypted(filepath.Join(dir, "secret.db"))
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

// demoEncrypted reuses the SAME Config shape, just adds an
// Encryption field. One defer db.Close() handles both the sql.DB
// pool AND the underlying encryption VFS.
func demoEncrypted(dbPath string) {
	fmt.Println("=== Encrypted database via sqlite.Config{Encryption: ...} ===")

	// Real apps load passphrase + salt from a keyring / env / KMS.
	// crypto.DeriveKey runs Argon2id with the authors' recommended
	// interactive-login parameters.
	passphrase := make([]byte, 32)
	salt := make([]byte, 16)
	io.ReadFull(rand.Reader, passphrase)
	io.ReadFull(rand.Reader, salt)
	key := crypto.DeriveKey(passphrase, salt, sqlite.Adiantum)

	db, err := sqlite.Open(sqlite.Config{
		Path:    dbPath,
		Pragmas: sqlite.RecommendedPragmas(),
		Encryption: &sqlite.Encryption{
			Key:    key,
			Cipher: sqlite.Adiantum, // default; shown for clarity
		},
		MaxOpenConns:    4,
		ConnMaxLifetime: 5 * time.Minute,
	})
	if err != nil {
		log.Fatalf("sqlite.Open: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`
		CREATE TABLE secrets (
			id   INTEGER PRIMARY KEY,
			body TEXT NOT NULL
		);
		INSERT INTO secrets (body) VALUES ('this row is ciphertext on disk');
	`); err != nil {
		log.Fatalf("CREATE/INSERT: %v", err)
	}

	var body string
	db.QueryRow(`SELECT body FROM secrets`).Scan(&body)
	fmt.Printf("decrypted: %q\n", body)
	fmt.Printf("Config registered encryption VFS: %q\n", db.VFSName())

	// Check the on-disk file — SQLite magic should NOT be present.
	// We close + reopen the file from raw bytes to demonstrate.
	if err := db.Close(); err != nil {
		log.Fatalf("Close: %v", err)
	}
	raw, _ := os.ReadFile(dbPath)
	if len(raw) >= 16 && string(raw[:15]) == "SQLite format 3" {
		fmt.Println("WARN: on-disk file leaks SQLite header (encryption not engaged)")
	} else {
		fmt.Println("on-disk: SQLite header not visible (encrypted)")
	}
}
