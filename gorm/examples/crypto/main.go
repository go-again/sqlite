// gorm-crypto: gorm on an encrypted-at-rest SQLite database.
//
// One Go-typed sqlite.Config, opened via sqlitegorm.OpenConfig, wires
// Argon2id key derivation, the vfs/crypto encryption VFS (Adiantum), the
// recommended WAL + busy-timeout pragmas, and per-page IO observability —
// with no DSN string assembly. db.Close() drains the pool AND unregisters
// the encryption VFS in the safe order.
//
// Run with:
//
//	go run .
package main

import (
	"bytes"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	sqlite "gosqlite.org"
	sqlitegorm "gosqlite.org/gorm"
	"gosqlite.org/vfs/crypto"
)

type User struct {
	ID    uint   `gorm:"primaryKey"`
	Email string `gorm:"uniqueIndex;not null"`
	Notes []Note
}

type Note struct {
	ID     uint   `gorm:"primaryKey"`
	UserID uint   `gorm:"not null;index"`
	Title  string `gorm:"size:128;not null"`
	Body   string `gorm:"not null"`
}

func main() {
	// Real apps load the passphrase from the OS keyring / env / TPM and the
	// salt from a sibling file or KMS. Hard-coded here to stay self-contained.
	passphrase := []byte("not-a-real-secret")
	salt := []byte("per-db-salt-16by")
	key, err := crypto.DeriveKey(passphrase, salt, sqlite.Adiantum)
	if err != nil {
		log.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn, // crypto Recorder events fire at Debug; lower to inspect IO.
	}))

	dir, _ := os.MkdirTemp("", "gorm-crypto-*")
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "secret.db")

	// One Config opens an encrypted, WAL-mode gorm DB — no DSN assembly. The
	// same Config shape works with the root package's sqlite.Open without gorm.
	db, err := sqlitegorm.OpenConfig(sqlite.Config{
		Path:    dbPath,
		Pragmas: sqlite.RecommendedPragmas(),
		Encryption: &sqlite.Encryption{
			Key:      key,
			Cipher:   sqlite.Adiantum,
			Recorder: crypto.NewSlogRecorder(logger),
		},
	}, &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		log.Fatalf("sqlitegorm.OpenConfig: %v", err)
	}

	if err := db.AutoMigrate(&User{}, &Note{}); err != nil {
		log.Fatalf("AutoMigrate: %v", err)
	}

	alice := User{Email: "alice@example.com", Notes: []Note{
		{Title: "Arctic", Body: "polar bears live in the arctic and hunt seals"},
		{Title: "Desert", Body: "deserts host camels lizards and succulents"},
	}}
	if err := db.Create(&alice).Error; err != nil {
		log.Fatalf("create: %v", err)
	}

	var notes []Note
	if err := db.Where("user_id = ?", alice.ID).Order("title").Find(&notes).Error; err != nil {
		log.Fatalf("query: %v", err)
	}
	fmt.Printf("%s has %d notes:\n", alice.Email, len(notes))
	for _, n := range notes {
		fmt.Printf("  - %s\n", n.Title)
	}

	// Drain the pool + unregister the encryption VFS, then read the raw file
	// to prove it is genuinely encrypted: a plaintext body must NOT appear.
	if err := db.Close(); err != nil {
		log.Fatalf("close: %v", err)
	}
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		log.Fatal(err)
	}
	if bytes.Contains(raw, []byte("polar bears")) {
		log.Fatal("FAIL: plaintext found on disk — database is not encrypted")
	}
	fmt.Println("\nOn-disk file is encrypted (no plaintext leaked). ✓")
}
