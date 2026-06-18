// ext-spellfix1: fuzzy spell-correction with the typed spellfix1.Vocab
// API. Build a vocabulary, then map misspellings to their closest real
// word — no hand-written SQL. Vocab mirrors vec.Table / fts.Index, so the
// Create / Add / Correct / *SQL shape is the same across all three typed
// extensions.
//
// Run with:
//
//	just example spellfix1
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/spellfix1"
	_ "github.com/go-again/sqlite/ext/spellfix1/auto" // registers the vtab module on every conn
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer db.Close()
	// One connection keeps the in-memory vocabulary alive across calls;
	// the spellfix1/auto blank import above registers the module on it.
	db.SetMaxOpenConns(1)
	ctx := context.Background()

	// Typed CREATE VIRTUAL TABLE … USING spellfix1.
	vocab, err := spellfix1.Create(ctx, db, "words", spellfix1.WithIfNotExists())
	if err != nil {
		log.Fatalf("create: %v", err)
	}

	// Batch-insert the vocabulary in a single transaction. Re-adding a word
	// is a no-op (the vocabulary deduplicates), so streaming tokens from
	// many sources into one canonical set is safe.
	if err := vocab.AddMany(ctx, []string{
		"colour", "color", "apple", "apricot", "banana", "durable", "cherry",
	}); err != nil {
		log.Fatalf("addMany: %v", err)
	}
	n, _ := vocab.Size(ctx)
	fmt.Printf("vocabulary: %d distinct words\n\n", n)

	// Correct misspellings: the closest vocabulary word within edit
	// distance 2, ascending by distance.
	fmt.Println("query    -> correction (distance)")
	fmt.Println("---------+-----------------------")
	for _, q := range []string{"aple", "colur", "banaa", "durabl", "xyzzy"} {
		matches, err := vocab.Correct(ctx, q,
			spellfix1.WithMaxDistance(2), spellfix1.WithLimit(1))
		if err != nil {
			log.Fatalf("correct %q: %v", q, err)
		}
		if len(matches) == 0 {
			fmt.Printf("%-9s-> (no match within distance 2)\n", q)
			continue
		}
		m := matches[0]
		fmt.Printf("%-9s-> %s (d=%d)\n", q, m.Word, m.Distance)
	}

	// Tightening the distance bound drops far candidates.
	fmt.Println("\n'aple' candidates with WithMaxDistance(1):")
	near, _ := vocab.Correct(ctx, "aple", spellfix1.WithMaxDistance(1))
	for _, m := range near {
		fmt.Printf("  %s (d=%d)\n", m.Word, m.Distance)
	}

	// CorrectSQL exposes the query + bind args for callers who'd rather run
	// it through their own *sql.DB or gorm.Raw().Scan(...).
	q, args, _ := vocab.CorrectSQL("aple", spellfix1.WithMaxDistance(2), spellfix1.WithLimit(3))
	fmt.Printf("\nCorrectSQL: %s\n      args: %v\n", q, args)
}
