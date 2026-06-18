// gorm-fts-tagged: the tag-driven flow for combining gorm models with
// SQLite FTS5. Mark text fields with `fts5:"..."` on a model, register
// the ftsgorm.Plugin(), call ftsgorm.Migrate (which creates the FTS5
// external-content table + the AFTER INSERT/UPDATE/DELETE triggers),
// and let db.Create / ftsgorm.Search handle the index transparently.
//
// Compare with examples/features/gorm/fts/ for the side-by-side recipe — both
// patterns coexist; pick the one that fits.
package main

import (
	"context"
	"fmt"
	"log"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/fts"
	ftsgorm "github.com/go-again/sqlite/fts/gorm"
	sqlitegorm "github.com/go-again/sqlite/gorm"
)

// Article is a typical gorm model with two indexed text fields. Both
// are tagged with `fts5:` — they share one FTS5 external-content
// table named `articles_fts` (the default `<source>_fts` form).
type Article struct {
	ID    uint   `gorm:"primaryKey"`
	Title string `fts5:"tokenize=porter+unicode61"`
	Body  string `fts5:"tokenize=porter+unicode61"`
}

func main() {
	db, err := gorm.Open(sqlitegorm.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := db.Use(ftsgorm.Plugin()); err != nil {
		log.Fatal(err)
	}
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		log.Fatal(err)
	}

	// Seed a few articles. The AFTER INSERT trigger maintains the
	// FTS5 index automatically — no extra Go code.
	articles := []Article{
		{Title: "Hello world", Body: "The quick brown fox jumps over the lazy dog"},
		{Title: "Bears are bears", Body: "Polar bears live in the arctic"},
		{Title: "On dogs", Body: "Dogs are loyal companions"},
	}
	if err := db.Create(&articles).Error; err != nil {
		log.Fatal(err)
	}

	// Search for "fox" — single hit with snippet + highlight pulled
	// from the source via FTS5's documented auxiliary functions.
	ctx := context.Background()
	results, err := ftsgorm.Search[Article](
		ctx, db, fts.Term("fox"),
		ftsgorm.WithSnippet("body", "<b>", "</b>", "…", 8),
		ftsgorm.WithHighlight("body", "[", "]"),
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Search for 'fox':")
	for _, r := range results {
		fmt.Printf("  id=%d title=%q rank=%.4f\n", r.Model.ID, r.Model.Title, r.Rank)
		fmt.Printf("    snippet:   %s\n", r.Snippet)
		fmt.Printf("    highlight: %s\n", r.Highlight)
	}

	// Search across both columns with BM25 weights.
	results, err = ftsgorm.Search[Article](
		ctx, db, fts.Term("bears"),
		ftsgorm.WithRanking(2.0, 1.0), // weight title higher than body
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Search for 'bears' (title-weighted):")
	for _, r := range results {
		fmt.Printf("  id=%d title=%q\n", r.Model.ID, r.Model.Title)
	}

	// Cleanup: db.Migrator().DropTable also tears down the FTS5 table
	// and its three triggers through the plugin's DropTableHook.
	if err := db.Migrator().DropTable(&Article{}); err != nil {
		log.Fatal(err)
	}
	fmt.Println("DropTable cascade: source + FTS5 table + triggers all gone")
}
