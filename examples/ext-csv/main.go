// ext-csv: read CSV files as SQL tables with the typed csv.Table API.
// csv.Create hides the `USING csv(…)` argument string and its quoting (the
// way sqlite.Open hides a DSN); the rows are queried as SQL, since a CSV is
// schemaless. Table mirrors vec.Table / fts.Index / closure.Graph.
//
// Run with:
//
//	just example ext-csv
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"testing/fstest"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/csv"
)

func main() {
	// Bundle a small CSV fixture in an in-memory fs.FS so the example runs
	// without touching disk. For real workloads, swap in embed.FS,
	// os.DirFS(prefix), or blank-import ext/csv/auto for os.Open access.
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

	// The typed csv.Table API runs over a *sql.DB, so the csv module must be
	// on every pooled connection. Wire RegisterFS through a connection hook
	// (the auto sub-package would register os-backed access instead).
	sqlite.DefaultDriver().RegisterConnectionHook(func(c sqlite.ExecQuerierContext, _ string) error {
		return csv.RegisterFS(c.(*sqlite.Conn), fsys)
	})

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	// CREATE VIRTUAL TABLE … USING csv(filename=…, header=on), typed — no
	// hand-built argument string, no quoting footguns.
	sales, err := csv.Create(ctx, db, "sales", csv.WithFilename("sales.csv"), csv.WithHeader())
	if err != nil {
		log.Fatalf("create sales: %v", err)
	}
	if _, err := csv.Create(ctx, db, "reps", csv.WithFilename("reps.csv"), csv.WithHeader()); err != nil {
		log.Fatalf("create reps: %v", err)
	}

	cols, _ := sales.Columns(ctx)
	fmt.Printf("sales columns (from the CSV header): %v\n", cols)

	// CSV columns are schemaless TEXT, so SUM coerces the numeric strings.
	fmt.Println("\nPer-region totals (CSV + aggregate):")
	rows, _ := db.QueryContext(ctx, `
		SELECT region, SUM(amount) AS total
		FROM `+sales.Name()+` GROUP BY region ORDER BY total DESC`)
	for rows.Next() {
		var r string
		var total float64
		_ = rows.Scan(&r, &total)
		fmt.Printf("  %-6s %.2f\n", r, total)
	}
	rows.Close()

	// Joining and filtering a CSV is the vtab's whole point — that stays
	// plain SQL. CAST gives the TEXT amount column numeric ordering.
	fmt.Println("\nJOIN sales × reps (CSV × CSV):")
	rows, _ = db.QueryContext(ctx, `
		SELECT reps.rep, sales.quarter, sales.amount
		FROM sales JOIN reps USING (region)
		WHERE CAST(sales.amount AS REAL) > 9000
		ORDER BY CAST(sales.amount AS REAL) DESC`)
	for rows.Next() {
		var rep, q string
		var amt float64
		_ = rows.Scan(&rep, &q, &amt)
		fmt.Printf("  %-6s %s  $%.2f\n", rep, q, amt)
	}
	rows.Close()
}
