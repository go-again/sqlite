// gorm-crypto: a comprehensive end-to-end example wiring every
// layer you'd realistically want in a production gorm app that
// needs encryption at rest.
//
// What it shows:
//   - sqlite.Config / sqlite.Pragmas / sqlite.Encryption — one
//     Config type at the root package, consumed by both
//     database/sql users and gorm users. No DSN string assembly.
//   - crypto.DeriveKey: Argon2id passphrase + per-DB salt → key.
//   - sqlitegorm.OpenConfig: gorm-idiomatic open that takes the
//     SAME Config. One defer db.Close() releases gorm pool AND
//     encryption VFS in the safe order.
//   - vfs/crypto registered with a slog Recorder so every page-
//     level read/write/sync is observable.
//   - vec/gorm and fts/gorm plugins coexisting on the same model.
//     Each row carries a 4-D embedding AND a tokenized FTS5 body;
//     the embedding sidecar, the FTS5 external-content table, and
//     the source `notes` table all land in the encrypted DB.
//   - Hybrid search: rank by semantic similarity (vec.KNN) AND by
//     lexical match (fts.Search), fuse via fusion.RRF2.
//
// Run with:
//
//	just example gorm-crypto
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/fts"
	ftsgorm "github.com/go-again/sqlite/fts/gorm"
	"github.com/go-again/sqlite/fusion"
	sqlitegorm "github.com/go-again/sqlite/gorm"
	vecgorm "github.com/go-again/sqlite/vec/gorm"
	"github.com/go-again/sqlite/vfs/crypto"
)

// Note is the application model. One field is FTS5-indexed; one is
// vector-indexed. gorm sees a plain table; the plugins quietly own
// the FTS5 + vec0 sidecars in the same encrypted database.
type Note struct {
	ID        uint              `gorm:"primaryKey"`
	UserID    uint              `gorm:"not null;index"`
	Title     string            `gorm:"size:128;not null"`
	Body      string            `gorm:"not null"                 fts5:"tokenize=porter+unicode61"`
	Embedding vecgorm.Embedding `vec:"dim=4;metric=cosine"`
}

// User exists so we can demonstrate that ordinary foreign-key
// relations work unchanged inside an encrypted DB.
type User struct {
	ID    uint   `gorm:"primaryKey"`
	Email string `gorm:"uniqueIndex;not null"`
	Notes []Note
}

func main() {
	// Real apps load the passphrase from the OS keyring / env / TPM
	// and the salt from a sibling file or KMS. Hard-coded here to
	// keep the example self-contained.
	passphrase := []byte("not-a-real-secret")
	salt := []byte("per-db-salt-16by")
	key := crypto.DeriveKey(passphrase, salt, sqlite.Adiantum)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn, // Recorder events fire at Debug; bump for diagnosis.
	}))

	dir, _ := os.MkdirTemp("", "gorm-crypto-*")
	defer os.RemoveAll(dir)

	// One Go-typed Config opens an encrypted, WAL-mode, busy-
	// timeout-equipped gorm DB. No DSN string assembly. The same
	// Config shape works against the root package's sqlite.Open
	// if you don't need gorm.
	db, err := sqlitegorm.OpenConfig(sqlite.Config{
		Path:    filepath.Join(dir, "secret.db"),
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
	// Single defer drains the *sql.DB then unregisters the VFS in
	// the documented safe order.
	defer db.Close()

	// Register the vec and fts plugins. Order doesn't matter.
	if err := db.Use(vecgorm.Plugin()); err != nil {
		log.Fatalf("vecgorm.Plugin: %v", err)
	}
	if err := db.Use(ftsgorm.Plugin()); err != nil {
		log.Fatalf("ftsgorm.Plugin: %v", err)
	}

	// Migrate the gorm-visible tables, then the sidecars.
	if err := db.AutoMigrate(&User{}, &Note{}); err != nil {
		log.Fatalf("AutoMigrate: %v", err)
	}
	if err := vecgorm.Migrate(db.DB, &Note{}); err != nil {
		log.Fatalf("vecgorm.Migrate: %v", err)
	}
	if err := ftsgorm.Migrate(db.DB, &Note{}); err != nil {
		log.Fatalf("ftsgorm.Migrate: %v", err)
	}

	// Seed a user with a handful of notes.
	alice := User{Email: "alice@example.com"}
	if err := db.Create(&alice).Error; err != nil {
		log.Fatalf("create user: %v", err)
	}
	notes := []Note{
		{UserID: alice.ID, Title: "Arctic", Body: "polar bears live in the arctic and hunt seals", Embedding: vecgorm.Embedding{0, 1, 0, 0}},
		{UserID: alice.ID, Title: "Desert", Body: "deserts host camels lizards and resilient succulents", Embedding: vecgorm.Embedding{1, 0, 0, 0}},
		{UserID: alice.ID, Title: "Forest", Body: "deep forests harbor brown bears wolves and rare birds", Embedding: vecgorm.Embedding{0, 0.7, 0.7, 0}},
		{UserID: alice.ID, Title: "Ocean", Body: "polar oceans freeze over each winter feeding seabirds", Embedding: vecgorm.Embedding{0, 0.5, 0, 0.5}},
		{UserID: alice.ID, Title: "Tundra", Body: "the tundra hosts arctic foxes and grazing reindeer", Embedding: vecgorm.Embedding{0, 0.9, 0.1, 0}},
	}
	if err := db.Create(&notes).Error; err != nil {
		log.Fatalf("create notes: %v", err)
	}
	fmt.Printf("Seeded %d notes for %s\n\n", len(notes), alice.Email)

	ctx := context.Background()

	// Semantic ranker.
	vecHits, err := vecgorm.KNN[Note](ctx, db.DB, []float32{0, 0.95, 0, 0}, 5)
	if err != nil {
		log.Fatalf("vec KNN: %v", err)
	}
	fmt.Println("Vector ranker (cold/polar):")
	vecKeys := make([]uint, len(vecHits))
	for i, h := range vecHits {
		vecKeys[i] = h.Model.ID
		fmt.Printf("  %d. id=%d title=%-7q distance=%.4f\n", i+1, h.Model.ID, h.Model.Title, h.Distance)
	}

	// Lexical ranker.
	ftsHits, err := ftsgorm.Search[Note](ctx, db.DB, fts.Or(fts.Term("bears"), fts.Term("arctic")))
	if err != nil {
		log.Fatalf("fts Search: %v", err)
	}
	fmt.Println("\nLexical ranker (bears OR arctic):")
	ftsKeys := make([]uint, len(ftsHits))
	for i, h := range ftsHits {
		ftsKeys[i] = h.Model.ID
		fmt.Printf("  %d. id=%d title=%-7q rank=%.4f\n", i+1, h.Model.ID, h.Model.Title, h.Rank)
	}

	// Hybrid: fuse the two rankings with RRF.
	fmt.Println("\nFused ranking (RRF over both):")
	fused := fusion.RRF2(vecKeys, ftsKeys, fusion.WithLimit(5))
	for i, r := range fused {
		var n Note
		db.Select("id, title").First(&n, r.Key)
		fmt.Printf("  %d. id=%d title=%-7q rrf=%.5f\n", i+1, n.ID, n.Title, r.Score)
	}

	// Prove the on-disk file really is encrypted.
	raw, _ := os.ReadFile(filepath.Join(dir, "secret.db"))
	if len(raw) >= 16 && string(raw[:15]) == "SQLite format 3" {
		fmt.Println("\nWARN: on-disk file leaks SQLite magic — encryption is NOT engaged")
	} else {
		fmt.Println("\non-disk: SQLite magic not visible (encrypted)")
	}
}
