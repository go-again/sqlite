// ext-text: rune-aware string functions SQLite lacks — text_reverse,
// text_repeat, text_lpad / text_rpad, text_split — via the ext/text scalars.
//
// Run with:
//
//	just example text
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	_ "gosqlite.org/ext/text/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	for _, q := range []struct{ label, sql string }{
		{"text_reverse('abçd') (rune-aware)", `SELECT text_reverse('abçd')`},
		{"text_repeat('ab', 3)", `SELECT text_repeat('ab', 3)`},
		{"text_lpad('7', 4, '0')", `SELECT text_lpad('7', 4, '0')`},
		{"text_rpad('x', 4, '.')", `SELECT text_rpad('x', 4, '.')`},
		{"text_split('a,b,c', ',', 2)", `SELECT text_split('a,b,c', ',', 2)`},
	} {
		var got string
		if err := db.QueryRowContext(ctx, q.sql).Scan(&got); err != nil {
			log.Fatalf("%s: %v", q.label, err)
		}
		fmt.Printf("  %-34s => %q\n", q.label, got)
	}

	// A hostile count is rejected, not allowed to exhaust memory.
	var s string
	err = db.QueryRowContext(ctx, `SELECT text_repeat('x', 9223372036854775807)`).Scan(&s)
	fmt.Printf("\n  text_repeat('x', <huge>) safely errors: %v\n", err != nil)
}
