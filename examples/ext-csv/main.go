// ext-csv: SELECT directly from a CSV file as if it were a SQL table.
// Run with: just example ext-csv
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"testing/fstest"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/csv"
)

func main() {
	// Bundle a small CSV fixture in an in-memory fs.FS so the example
	// runs without touching disk. For real workloads, swap in either
	// embed.FS, os.DirFS(prefix), or call csv.Register(conn) for direct
	// os.Open access.
	fsys := fstest.MapFS{
		"sales.csv": {Data: []byte(`region,quarter,amount
North,Q1,12000
North,Q2,13500
South,Q1,8000.50
South,Q2,9200.00
East,Q1,15000
West,Q1,7250.75
West,Q2,8100.00
`)},
		"reps.csv": {Data: []byte(`region,rep
North,Alice
South,Bob
East,Carol
West,Dave
`)},
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer sc.Close()

	// Register the csv module with our in-memory fs.FS so filename=...
	// resolves inside the sandbox.
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return csv.RegisterFS(c, fsys)
	}); err != nil {
		log.Fatal(err)
	}

	if _, err := sc.ExecContext(ctx, `
		CREATE VIRTUAL TABLE temp.sales USING csv(
		    filename='sales.csv', header=on,
		    schema='CREATE TABLE x(region TEXT, quarter TEXT, amount REAL)');
		CREATE VIRTUAL TABLE temp.reps USING csv(
		    filename='reps.csv', header=on);`); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Per-region totals (CSV + aggregate):")
	rows, _ := sc.QueryContext(ctx, `
		SELECT region, SUM(amount) AS total
		FROM temp.sales GROUP BY region ORDER BY total DESC`)
	for rows.Next() {
		var r string
		var total float64
		_ = rows.Scan(&r, &total)
		fmt.Printf("  %-6s %.2f\n", r, total)
	}
	rows.Close()

	fmt.Println("\nJOIN sales × reps (CSV × CSV):")
	rows, _ = sc.QueryContext(ctx, `
		SELECT reps.rep, sales.quarter, sales.amount
		FROM temp.sales JOIN temp.reps USING (region)
		WHERE sales.amount > 9000
		ORDER BY sales.amount DESC`)
	for rows.Next() {
		var rep, q string
		var amt float64
		_ = rows.Scan(&rep, &q, &amt)
		fmt.Printf("  %-6s %s  $%.2f\n", rep, q, amt)
	}
	rows.Close()
}
