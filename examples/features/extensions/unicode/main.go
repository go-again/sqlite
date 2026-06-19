// ext-unicode: Unicode-aware string SQL functions + collations.
// Run with: just example unicode
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	sqlite "gosqlite.org"
	"gosqlite.org/ext/unicode"
	_ "gosqlite.org/ext/unicode/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	fmt.Println("Case mapping:")
	for _, q := range []string{
		`SELECT upper('café')`,
		`SELECT lower('STRASSE')`,
		`SELECT upper('straße', 'de')`,   // German eszett → SS
		`SELECT lower('İSTANBUL', 'tr')`, // Turkish capital İ → dotted i
		`SELECT initcap('hello unicode world')`,
		`SELECT casefold('GROẞER')`,
	} {
		var got string
		_ = db.QueryRowContext(ctx, q).Scan(&got)
		fmt.Printf("  %s → %q\n", q, got)
	}

	fmt.Println("\nNormalization + unaccent:")
	for _, q := range []string{
		`SELECT length(normalize('café', 'NFC'))`,
		`SELECT length(normalize('café', 'NFD'))`,
		`SELECT unaccent('résumé')`,
		`SELECT unaccent('naïve Björk')`,
	} {
		var got any
		_ = db.QueryRowContext(ctx, q).Scan(&got)
		fmt.Printf("  %s → %v\n", q, got)
	}

	// Collations — register a Swedish-locale collation alongside the
	// presets that auto/ already installed.
	sc, _ := db.Conn(ctx)
	defer sc.Close()
	_ = sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return unicode.RegisterLocaleCollation(c, "sv", "SV")
	})

	if _, err := sc.ExecContext(ctx, `
		CREATE TABLE words(s TEXT);
		INSERT INTO words(s) VALUES ('Apple'), ('apple'), ('åpple'), ('zebra')`); err != nil {
		log.Fatal(err)
	}

	fmt.Println("\nNOCASE_UNICODE collation (case-insensitive, accent-sensitive):")
	rows, _ := sc.QueryContext(ctx,
		`SELECT s FROM words WHERE s = 'apple' COLLATE NOCASE_UNICODE`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		fmt.Printf("  %s\n", s)
	}
	rows.Close()

	fmt.Println("\nSwedish collation (sv): 'å' sorts after 'z':")
	rows, _ = sc.QueryContext(ctx, `SELECT s FROM words ORDER BY s COLLATE SV`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		fmt.Printf("  %s\n", s)
	}
	rows.Close()
}
