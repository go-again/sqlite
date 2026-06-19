// ext-pivot: build a cross-tab via the pivot vtab. Three SELECT
// statements drive the schema: row-keys, column-keys (value + display
// name), and the per-cell aggregate.
//
// Run with:
//
//	just example pivot
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	sqlite "gosqlite.org"
	_ "gosqlite.org/ext/pivot/auto"
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

	if _, err := sc.ExecContext(ctx,
		`CREATE TABLE sales(region TEXT, product TEXT, units INTEGER)`); err != nil {
		log.Fatalf("CREATE: %v", err)
	}
	rows := [][]any{
		{"north", "apple", 10},
		{"north", "banana", 5},
		{"south", "apple", 20},
		{"south", "banana", 7},
		{"south", "cherry", 3},
		{"east", "apple", 14},
	}
	for _, r := range rows {
		if _, err := sc.ExecContext(ctx, `INSERT INTO sales VALUES (?, ?, ?)`, r...); err != nil {
			log.Fatalf("INSERT: %v", err)
		}
	}

	// Pivot: one row per region, one column per product.
	if _, err := sc.ExecContext(ctx, `
		CREATE VIRTUAL TABLE p USING pivot(
		    'SELECT DISTINCT region FROM sales ORDER BY region',
		    'SELECT product, product FROM (SELECT DISTINCT product FROM sales ORDER BY product)',
		    'SELECT SUM(units) FROM sales WHERE region = ? AND product = ?'
		)`); err != nil {
		log.Fatalf("CREATE VTAB: %v", err)
	}

	fmt.Println("Sales by region × product:")
	fmt.Println("region | apple | banana | cherry")
	fmt.Println("-------+-------+--------+--------")

	r2, err := sc.QueryContext(ctx, `SELECT region, apple, banana, cherry FROM p ORDER BY region`)
	if err != nil {
		log.Fatalf("Query: %v", err)
	}
	defer r2.Close()
	for r2.Next() {
		var region string
		var apple, banana, cherry sql.NullInt64
		if err := r2.Scan(&region, &apple, &banana, &cherry); err != nil {
			log.Fatalf("Scan: %v", err)
		}
		fmt.Printf("%-6s | %s | %s | %s\n", region, fmtNull(apple), fmtNull(banana), fmtNull(cherry))
	}
}

func fmtNull(n sql.NullInt64) string {
	if !n.Valid {
		return "  -  "
	}
	return fmt.Sprintf("%5d", n.Int64)
}
