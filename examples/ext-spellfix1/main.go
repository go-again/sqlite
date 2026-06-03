// ext-spellfix1: build a vocabulary, then look up misspellings via
// `WHERE word MATCH ?` — Soundex prefilter + Damerau-Levenshtein
// scoring, with optional rank boost.
//
// Run with:
//
//	just example ext-spellfix1
package main

import (
	"context"
	"fmt"
	"log"

	sqlite "github.com/go-again/sqlite"
	_ "github.com/go-again/sqlite/ext/spellfix1/auto"
)

func main() {
	db, err := sqlite.Open(sqlite.Config{Path: ":memory:"})
	if err != nil {
		log.Fatalf("Open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatalf("Conn: %v", err)
	}
	defer sc.Close()

	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE words USING spellfix1`); err != nil {
		log.Fatalf("CREATE VTAB: %v", err)
	}

	// Seed a small vocabulary with rank weights (higher rank → preferred
	// match when distances tie).
	seed := []struct {
		word string
		rank int
	}{
		{"colour", 100},
		{"color", 10},
		{"apple", 50},
		{"apricot", 30},
		{"banana", 40},
		{"durable", 20},
	}
	for _, e := range seed {
		if _, err := sc.ExecContext(ctx,
			`INSERT INTO words(word, rank) VALUES (?, ?)`, e.word, e.rank); err != nil {
			log.Fatalf("INSERT %s: %v", e.word, err)
		}
	}

	queries := []string{"aple", "colur", "banaa", "durabl"}
	fmt.Println("query | top match | distance | rank")
	fmt.Println("------+-----------+----------+-----")
	for _, q := range queries {
		var word string
		var dist, rank int
		err := sc.QueryRowContext(ctx,
			`SELECT word, distance, rank FROM words WHERE word MATCH ? LIMIT 1`, q,
		).Scan(&word, &dist, &rank)
		if err != nil {
			fmt.Printf("%-6s| (no match: %v)\n", q, err)
			continue
		}
		fmt.Printf("%-6s| %-9s | %-8d | %d\n", q, word, dist, rank)
	}

	// scope cap: pinning max edit-distance.
	fmt.Println()
	fmt.Println("Tight scope (scope=1) drops far matches:")
	rows, err := sc.QueryContext(ctx,
		`SELECT word, distance FROM words WHERE word MATCH 'aple' AND scope = 1`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var w string
		var d int
		_ = rows.Scan(&w, &d)
		fmt.Printf("  %s (d=%d)\n", w, d)
	}
}
