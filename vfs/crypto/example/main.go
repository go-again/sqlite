// crypto example: open a SQLite database with transparent at-rest
// encryption via crypto.Open. The file on disk is Adiantum ciphertext;
// the caller sees a regular *sqlite.DB (embeds *sql.DB), and a single
// defer db.Close() releases both the pool and the encrypting VFS.
//
// Run from the crypto module:
//
//	cd vfs/crypto && go run ./example
package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs/crypto"
)

func main() {
	// 32-byte key for Adiantum (the default cipher). In production this comes
	// from a keyring / HSM / an argon2id-derived passphrase (crypto.DeriveKey).
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		log.Fatal(err)
	}

	dir, _ := os.MkdirTemp("", "vfs-crypto-example-*")
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "secret.db")

	// crypto.Open registers the encrypting VFS, routes the Config through it,
	// and bundles VFS teardown into db.Close(). Recorder is optional; the slog
	// one emits a structured event per io-method invocation.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	db, err := crypto.Open(
		sqlite.Config{Path: dbPath, Pragmas: sqlite.RecommendedPragmas()},
		crypto.Options{Key: key, Recorder: crypto.NewSlogRecorder(logger)},
	)
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

	// Flush to disk, then confirm the row text is not visible in the raw file.
	if err := db.Close(); err != nil {
		log.Fatal(err)
	}
	raw, _ := os.ReadFile(dbPath)
	if bytes.Contains(raw, []byte("this row is encrypted on disk")) {
		fmt.Println("WARN: row text visible in raw file bytes (should not happen)")
	} else {
		fmt.Println("on-disk: row text not visible (encrypted)")
	}
}
