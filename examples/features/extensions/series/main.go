// ext-series: generate_series(start, stop[, step]) integer sequences via the
// ext/series table-valued function — usable anywhere a table is, including
// joins and aggregates.
//
// Run with:
//
//	just example series
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	_ "gosqlite.org/ext/series/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	fmt.Println("generate_series(1, 5):")
	fmt.Printf("  %v\n", collect(ctx, db, `SELECT value FROM generate_series(1, 5)`))

	fmt.Println("generate_series(0, 20, 5):")
	fmt.Printf("  %v\n", collect(ctx, db, `SELECT value FROM generate_series(0, 20, 5)`))

	fmt.Println("generate_series(5, 1, -1) (descending):")
	fmt.Printf("  %v\n", collect(ctx, db, `SELECT value FROM generate_series(5, 1, -1)`))

	// As an aggregate source.
	var sum int64
	if err := db.QueryRowContext(ctx,
		`SELECT sum(value) FROM generate_series(1, 100)`).Scan(&sum); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nsum of 1..100 = %d\n", sum)

	// As a left-join calendar to find gaps in a sparse table.
	if _, err := db.ExecContext(ctx, `CREATE TABLE present(n INTEGER); INSERT INTO present VALUES (1),(2),(4),(5)`); err != nil {
		log.Fatal(err)
	}
	fmt.Println("\nmissing values in 1..5:")
	fmt.Printf("  %v\n", collect(ctx, db,
		`SELECT value FROM generate_series(1, 5) LEFT JOIN present ON n = value WHERE n IS NULL`))
}

func collect(ctx context.Context, db *sql.DB, query string) []int64 {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			log.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}
