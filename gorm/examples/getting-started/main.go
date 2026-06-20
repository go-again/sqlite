// gorm: opens a gorm.DB via the modern Go-typed [sqlite.Config] API —
// same Config shape as the root sqlite.Open, just routed through gorm via
// sqlitegorm.OpenConfig. No DSN string assembly anywhere.
//
// The returned *sqlitegorm.DB embeds *gorm.DB, so every gorm method
// (db.AutoMigrate, db.Create, db.Use, db.Transaction, ...) works unchanged,
// and a single defer db.Close() releases gorm's connection pool.
//
// The gorm dialector has no encryption path; for an encrypted database with
// an ORM, use LiteORM (https://liteorm.org), built on this driver.
//
// Run with:
//
//	just example gorm
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	sqlite "gosqlite.org"
	sqlitegorm "gosqlite.org/gorm"
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
