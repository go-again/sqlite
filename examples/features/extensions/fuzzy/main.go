// ext-fuzzy: approximate string-matching SQL functions — edit distances,
// Jaro / Jaro-Winkler similarity, and Soundex phonetic codes — via ext/fuzzy.
//
// Run with:
//
//	just example fuzzy
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	_ "gosqlite.org/ext/fuzzy/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	fmt.Println("Distances and similarity:")
	for _, q := range []struct{ label, sql string }{
		{"levenshtein('kitten','sitting')", `SELECT levenshtein('kitten', 'sitting')`},
		{"damerau_levenshtein('ca','ac')", `SELECT damerau_levenshtein('ca', 'ac')`},
		{"hamming('karolin','kathrin')", `SELECT hamming('karolin', 'kathrin')`},
		{"jaro_winkler('MARTHA','MARHTA')", `SELECT printf('%.4f', jaro_winkler('MARTHA', 'MARHTA'))`},
		{"soundex('Robert')", `SELECT soundex('Robert')`},
		{"soundex('Rupert')", `SELECT soundex('Rupert')`},
	} {
		var got string
		if err := db.QueryRowContext(ctx, q.sql).Scan(&got); err != nil {
			log.Fatalf("%s: %v", q.label, err)
		}
		fmt.Printf("  %-34s => %s\n", q.label, got)
	}

	// Fuzzy lookup: rank a dictionary by edit distance from a typo.
	if _, err := db.ExecContext(ctx, `CREATE TABLE words(w TEXT)`); err != nil {
		log.Fatal(err)
	}
	for _, w := range []string{"apple", "apply", "ample", "maple", "grape"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO words VALUES (?)`, w); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Println("\nNearest dictionary words to the typo 'aple':")
	rows, err := db.QueryContext(ctx,
		`SELECT w, levenshtein(w, 'aple') AS d FROM words ORDER BY d, w LIMIT 3`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var w string
		var d int
		if err := rows.Scan(&w, &d); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %-8s distance %d\n", w, d)
	}
}
