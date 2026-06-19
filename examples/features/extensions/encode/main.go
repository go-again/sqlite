// ext-encode: encode(data, format) / decode(text, format) for the common
// binary-to-text codecs SQLite lacks (only hex is built in) — base64, base32,
// base16, ascii85, url — via the ext/encode scalars.
//
// Run with:
//
//	just example encode
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	_ "gosqlite.org/ext/encode/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	fmt.Println("encode('hello, sqlite ☺', format):")
	for _, format := range []string{"base64", "base32", "hex", "ascii85", "url"} {
		var enc string
		if err := db.QueryRowContext(ctx, `SELECT encode(?, ?)`, "hello, sqlite ☺", format).Scan(&enc); err != nil {
			log.Fatal(err)
		}
		// Round-trip back to confirm it decodes to the original bytes.
		var dec []byte
		if err := db.QueryRowContext(ctx, `SELECT decode(?, ?)`, enc, format).Scan(&dec); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %-8s %-28s (decodes back: %v)\n", format, enc, string(dec) == "hello, sqlite ☺")
	}

	// Malformed input surfaces as a SQL error.
	var b []byte
	err = db.QueryRowContext(ctx, `SELECT decode('@@@not base64@@@', 'base64')`).Scan(&b)
	fmt.Printf("\n  decode of malformed base64 errors: %v\n", err != nil)
}
