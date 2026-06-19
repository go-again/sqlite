// ext-bloom: probabilistic set-membership with the typed bloom.Filter
// API. Build a filter, then test membership without an exact lookup —
// "present" means probably-present (Bloom filters have a tunable
// false-positive rate), "absent" is definitive. Filter mirrors
// vec.Table / fts.Index / spellfix1.Vocab.
//
// Run with:
//
//	just example bloom
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	"gosqlite.org/ext/bloom"
	_ "gosqlite.org/ext/bloom/auto" // registers the vtab module on every conn
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer db.Close()
	// One connection keeps the in-memory filter alive across calls.
	db.SetMaxOpenConns(1)
	ctx := context.Background()

	// Typed CREATE VIRTUAL TABLE … USING bloom(size=…, p=…) — the arg
	// string and its quoting are hidden behind options.
	seen, err := bloom.Create(ctx, db, "seen",
		bloom.WithSize(10000), bloom.WithFalsePositiveRate(0.01), bloom.WithIfNotExists())
	if err != nil {
		log.Fatalf("create: %v", err)
	}

	// Add a known set in a single transaction.
	if err := seen.AddMany(ctx, []string{
		"alice@example.com", "bob@example.com", "carol@example.com",
	}); err != nil {
		log.Fatalf("addMany: %v", err)
	}

	fmt.Println("membership (present = probably-present, absent = definitely-not):")
	for _, addr := range []string{
		"alice@example.com",   // added
		"carol@example.com",   // added
		"mallory@example.com", // never added
		"trent@example.com",   // never added
	} {
		ok, err := seen.Contains(ctx, addr)
		if err != nil {
			log.Fatalf("contains %q: %v", addr, err)
		}
		verdict := "absent"
		if ok {
			verdict = "present"
		}
		fmt.Printf("  %-22s -> %s\n", addr, verdict)
	}

	fmt.Println("\nA Bloom filter never reports a false negative: every added key")
	fmt.Println("tests present. A 'present' for an un-added key is a false positive,")
	fmt.Println("bounded by the configured p (1% here).")
}
