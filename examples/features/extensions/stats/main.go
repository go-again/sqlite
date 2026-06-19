// ext-stats: statistical aggregates and window functions over a small
// employees table. Run with: just example stats
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	_ "gosqlite.org/ext/stats/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE employees(dept TEXT, salary REAL, tenure REAL);
		INSERT INTO employees(dept, salary, tenure) VALUES
		    ('eng', 95000, 5.5), ('eng', 110000, 7.2), ('eng', 130000, 9.8),
		    ('eng', 85000, 2.1), ('eng', 145000, 12.0),
		    ('sales', 60000, 1.5), ('sales', 75000, 3.2),
		    ('sales', 90000, 6.0), ('sales', 105000, 8.5);`); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Per-department stats:")
	rows, _ := db.QueryContext(ctx, `
		SELECT dept,
		    COUNT(*),
		    median(salary),
		    var_pop(salary),
		    corr(salary, tenure),
		    regr_slope(salary, tenure),
		    regr_intercept(salary, tenure)
		FROM employees GROUP BY dept`)
	for rows.Next() {
		var dept string
		var n int
		var med, varp, corr, slope, intercept float64
		_ = rows.Scan(&dept, &n, &med, &varp, &corr, &slope, &intercept)
		fmt.Printf("  %-6s n=%d  median=%.0f  var=%.0f  corr=%.3f  slope=%.0f  intercept=%.0f\n",
			dept, n, med, varp, corr, slope, intercept)
	}
	rows.Close()

	fmt.Println("\nSliding regr_slope(salary, tenure) over 3-row window:")
	rows, _ = db.QueryContext(ctx, `
		SELECT tenure, salary, regr_slope(salary, tenure) OVER (
		    ORDER BY tenure ROWS BETWEEN 1 PRECEDING AND 1 FOLLOWING
		) FROM employees`)
	for rows.Next() {
		var tenure, salary float64
		var slope sql.NullFloat64
		_ = rows.Scan(&tenure, &salary, &slope)
		if slope.Valid {
			fmt.Printf("  tenure=%4.1f salary=%6.0f  slope=%.0f\n", tenure, salary, slope.Float64)
		} else {
			fmt.Printf("  tenure=%4.1f salary=%6.0f  slope=NULL (frame too small)\n", tenure, salary)
		}
	}
	rows.Close()

	fmt.Println("\nMulti-percentile via JSON array:")
	var quartiles string
	_ = db.QueryRowContext(ctx,
		`SELECT percentile_cont(salary, '[0.25, 0.5, 0.75]') FROM employees`).Scan(&quartiles)
	fmt.Printf("  salary quartiles = %s\n", quartiles)

	fmt.Println("\nMode + every:")
	var modeDept string
	var allHigh bool
	_ = db.QueryRowContext(ctx, `SELECT mode(dept) FROM employees`).Scan(&modeDept)
	_ = db.QueryRowContext(ctx, `SELECT every(salary > 50000) FROM employees`).Scan(&allHigh)
	fmt.Printf("  most-common dept: %q   every(salary > 50000) = %v\n", modeDept, allHigh)
}
