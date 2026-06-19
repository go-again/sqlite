// fts-tokenizer: a custom Go FTS5 tokenizer. SQLite's built-in tokenizers
// treat "getUserName" as one token; this one splits camelCase / snake_case
// identifiers into their words, so a search for "user" finds it. No other
// pure-Go SQLite driver lets you write the tokenizer in Go.
//
// Run with:
//
//	just example fts-tokenizer
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"unicode"

	sqlite "gosqlite.org"
)

// identTokenizer splits programming identifiers into lowercased word tokens:
// "getUserName_v2" → get, user, name, v2. Each token reports the byte span it
// came from so snippet/highlight still point at the source text.
type identTokenizer struct{}

func (identTokenizer) Tokenize(text string, emit func(token string, start, end int) error) error {
	runes := []rune(text)
	// Map rune index → byte offset so emit gets byte spans.
	byteOf := make([]int, len(runes)+1)
	b := 0
	for i, r := range runes {
		byteOf[i] = b
		b += len(string(r))
	}
	byteOf[len(runes)] = b

	wordStart := -1
	flush := func(end int) error {
		if wordStart < 0 {
			return nil
		}
		tok := strings.ToLower(string(runes[wordStart:end]))
		err := emit(tok, byteOf[wordStart], byteOf[end])
		wordStart = -1
		return err
	}
	for i, r := range runes {
		switch {
		case !(unicode.IsLetter(r) || unicode.IsDigit(r)):
			if err := flush(i); err != nil { // separator: end the word
				return err
			}
		case i > 0 && unicode.IsUpper(r) && unicode.IsLower(runes[i-1]):
			if err := flush(i); err != nil { // camelCase boundary
				return err
			}
			wordStart = i
		default:
			if wordStart < 0 {
				wordStart = i
			}
		}
	}
	return flush(len(runes))
}

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // FTS5 + per-conn tokenizer registration need one pinned conn.

	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer sc.Close()

	// Register the Go tokenizer on the connection.
	if err := sc.Raw(func(dc any) error {
		return dc.(*sqlite.Conn).RegisterFTS5Tokenizer("ident",
			func(args []string) (sqlite.FTS5Tokenizer, error) { return identTokenizer{}, nil })
	}); err != nil {
		log.Fatal(err)
	}

	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE code USING fts5(symbol, tokenize='ident')`); err != nil {
		log.Fatal(err)
	}
	for _, s := range []string{"getUserName", "setUserEmail", "parseHTTPHeader", "user_id_lookup"} {
		if _, err := sc.ExecContext(ctx, `INSERT INTO code(symbol) VALUES (?)`, s); err != nil {
			log.Fatal(err)
		}
	}

	for _, term := range []string{"user", "name", "http", "lookup"} {
		rows, err := sc.QueryContext(ctx, `SELECT symbol FROM code WHERE code MATCH ? ORDER BY symbol`, term)
		if err != nil {
			log.Fatal(err)
		}
		var hits []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				log.Fatal(err)
			}
			hits = append(hits, s)
		}
		rows.Close()
		fmt.Printf("  MATCH %-8q => %v\n", term, hits)
	}
}
