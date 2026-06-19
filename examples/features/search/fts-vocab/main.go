// fts-vocab: term-frequency analytics and autocomplete over an FTS5 index via
// the typed fts.Vocab (an fts5vocab view of the index's term dictionary).
//
// Run with:
//
//	just example fts-vocab
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	"gosqlite.org/fts"
)

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // FTS5 virtual tables are per-conn.

	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{})
	if err != nil {
		log.Fatal(err)
	}
	corpus := []string{
		"the quick brown fox",
		"the lazy brown dog",
		"a quick red fox jumps",
		"brown bears and brown foxes",
	}
	for i, body := range corpus {
		if err := idx.Insert(ctx, fts.Attr[int64, string]{Key: int64(i + 1), Value: body}); err != nil {
			log.Fatal(err)
		}
	}

	// Build a row-kind vocab and rank terms by frequency.
	vocab, err := fts.NewVocab(ctx, db, "docs", fts.VocabRow)
	if err != nil {
		log.Fatal(err)
	}
	top, err := vocab.TopTerms(ctx, 4)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Most frequent terms (term: documents / total occurrences):")
	for _, t := range top {
		fmt.Printf("  %-8s %d docs, %d occurrences\n", t.Term, t.Documents, t.Occurrences)
	}

	// Autocomplete: surface terms that start with a prefix, busiest first.
	fmt.Println("\nAutocomplete for prefix 'bro':")
	all, err := vocab.Terms(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, t := range all {
		if len(t.Term) >= 3 && t.Term[:3] == "bro" {
			fmt.Printf("  %s (%d)\n", t.Term, t.Occurrences)
		}
	}
}
