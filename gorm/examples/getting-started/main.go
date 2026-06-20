// gorm: opens a gorm.DB via the modern Go-typed
// [sqlite.Config] API — same Config shape as the root sqlite.Open,
// just routed through gorm via sqlitegorm.OpenConfig. No DSN string
// assembly anywhere.
//
// Shows two flavors:
//
//  1. Plain gorm with the recommended Pragmas.
//  2. Encrypted gorm — same Config, one Encryption field added.
//
// The returned *sqlitegorm.DB embeds *gorm.DB, so every gorm method
// (db.AutoMigrate, db.Create, db.Use, db.Transaction, ...) works
// unchanged. A single defer db.Close() releases gorm's connection
// pool AND any encryption VFS the open registered.
//
// Run with:
//
//	just example gorm
package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	sqlite "gosqlite.org"
	sqlitegorm "gosqlite.org/gorm"
	"gosqlite.org/vfs/crypto"
)

type Note struct {
	ID     uint   `gorm:"primaryKey"`
	UserID uint   `gorm:"not null;index"`
	Body   string `gorm:"not null"`
}

type User struct {
	ID    uint   `gorm:"primaryKey"`
	Email string `gorm:"uniqueIndex;not null"`
	Notes []Note
}

func main() {
	dir, _ := os.MkdirTemp("", "gorm-config-*")
	defer os.RemoveAll(dir)

	demoPlain(filepath.Join(dir, "plain.db"))
	fmt.Println()
	demoEncrypted(filepath.Join(dir, "secret.db"))
}

// demoPlain: gorm via sqlite.Config — Pragmas preset, no DSN, no
// `_pragma=…` URL flags.
func demoPlain(dbPath string) {
	fmt.Println("=== Plain gorm.DB via sqlitegorm.OpenConfig ===")

	db, err := sqlitegorm.OpenConfig(sqlite.Config{
		Path:         dbPath,
		Pragmas:      sqlite.RecommendedPragmas(),
		MaxOpenConns: 4,
	}, &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		log.Fatalf("OpenConfig: %v", err)
	}
	defer db.Close()

	// db embeds *gorm.DB — every gorm method works.
	if err := db.AutoMigrate(&User{}, &Note{}); err != nil {
		log.Fatalf("AutoMigrate: %v", err)
	}
	alice := User{Email: "alice@example.com", Notes: []Note{
		{Body: "first note"}, {Body: "second note"},
	}}
	if err := db.Create(&alice).Error; err != nil {
		log.Fatalf("Create: %v", err)
	}

	var loaded User
	db.Preload("Notes").First(&loaded, alice.ID)
	fmt.Printf("Loaded user %d (%s) with %d notes\n", loaded.ID, loaded.Email, len(loaded.Notes))
	for _, n := range loaded.Notes {
		fmt.Printf("  - %s\n", n.Body)
	}

	// Confirm Pragmas applied.
	var mode string
	db.Raw(`PRAGMA journal_mode`).Scan(&mode)
	fmt.Printf("journal_mode=%s\n", mode)
}

// demoEncrypted: same shape, Encryption field added. Lifecycle is
// the same — one defer db.Close().
func demoEncrypted(dbPath string) {
	fmt.Println("=== Encrypted gorm.DB via sqlite.Config{Encryption: ...} ===")

	passphrase := make([]byte, 32)
	salt := make([]byte, 16)
	io.ReadFull(rand.Reader, passphrase)
	io.ReadFull(rand.Reader, salt)
	key, err := crypto.DeriveKey(passphrase, salt, sqlite.Adiantum)
	if err != nil {
		log.Fatal(err)
	}

	db, err := sqlitegorm.OpenConfig(sqlite.Config{
		Path:    dbPath,
		Pragmas: sqlite.RecommendedPragmas(),
		Encryption: &sqlite.Encryption{
			Key:    key,
			Cipher: sqlite.Adiantum,
		},
		MaxOpenConns:    4,
		ConnMaxLifetime: 5 * time.Minute,
	}, &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		log.Fatalf("OpenConfig: %v", err)
	}
	defer db.Close()

	if err := db.AutoMigrate(&User{}, &Note{}); err != nil {
		log.Fatalf("AutoMigrate: %v", err)
	}
	bob := User{Email: "bob@example.com", Notes: []Note{
		{Body: "this note ciphertext on disk"},
		{Body: "FK relations still work"},
	}}
	if err := db.Create(&bob).Error; err != nil {
		log.Fatalf("Create: %v", err)
	}

	var loaded User
	db.Preload("Notes").First(&loaded, bob.ID)
	fmt.Printf("Loaded user %d (%s) with %d notes\n", loaded.ID, loaded.Email, len(loaded.Notes))
	for _, n := range loaded.Notes {
		fmt.Printf("  - %s\n", n.Body)
	}
	fmt.Printf("Config registered encryption VFS: %q\n", db.VFSName())

	// Close before reading raw bytes so SQLite's WAL is checkpointed.
	if err := db.Close(); err != nil {
		log.Fatalf("Close: %v", err)
	}
	raw, _ := os.ReadFile(dbPath)
	if len(raw) >= 16 && string(raw[:15]) == "SQLite format 3" {
		fmt.Println("WARN: on-disk file leaks SQLite header")
	} else {
		fmt.Println("on-disk: SQLite header not visible (encrypted)")
	}
}
