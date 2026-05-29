// vfs-crypto example: open a SQLite database with transparent
// at-rest encryption. The file on disk is Adiantum ciphertext; the
// caller sees a regular *sql.DB.
package main

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs/crypto"
)

func main() {
	// 32-byte key for Adiantum. In production this would come from a
	// keyring / HSM / env var / argon2id-derived passphrase — the
	// package treats the bytes as opaque.
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		log.Fatal(err)
	}

	// Register the encrypting VFS. New returns a name to slot into
	// the DSN. The *FS handle owns libc allocations; defer Close.
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		log.Fatal(err)
	}
	defer fs.Close()

	dir, _ := os.MkdirTemp("", "vfs-crypto-example-*")
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "secret.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		log.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO notes (body) VALUES ('this row is encrypted on disk')`); err != nil {
		log.Fatal(err)
	}

	var body string
	if err := db.QueryRow(`SELECT body FROM notes WHERE id = 1`).Scan(&body); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("decrypted: %s\n", body)

	// Demonstrate the file on disk is not plaintext: a regular grep
	// for the row text returns nothing.
	raw, _ := os.ReadFile(dbPath)
	if containsPhrase(raw, "this row is encrypted on disk") {
		fmt.Println("WARN: row text visible in raw file bytes (should not happen)")
	} else {
		fmt.Println("on-disk: row text not visible (encrypted)")
	}
}

func containsPhrase(haystack []byte, needle string) bool {
	n := []byte(needle)
	for i := 0; i+len(n) <= len(haystack); i++ {
		match := true
		for j := range n {
			if haystack[i+j] != n[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
