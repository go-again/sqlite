// window-function example: register a Go window-function on a
// connection and use it inside SQL. The WindowAccumulator interface
// has Step / Inverse / Value — SQLite drives the three callbacks as
// the engine moves through frames. Final is an optional
// WindowFinalizer interface for accumulators that need cleanup; this
// pure-math one doesn't.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"

	sqlite "gosqlite.org"
)

// runningSum keeps the moving sum over a numeric column. Inverse
// undoes a row's contribution when it leaves the frame, so this
// works for any windowed plan including sliding frames where SQLite
// can't recompute from scratch.
type runningSum struct{ total float64 }

func (s *runningSum) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	s.total += toFloat(args[0])
	return nil
}
func (s *runningSum) Inverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	s.total -= toFloat(args[0])
	return nil
}
func (s *runningSum) Value(_ *sqlite.FunctionContext) (driver.Value, error) {
	return s.total, nil
}

func toFloat(v driver.Value) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	}
	return 0
}

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // pin so the registered UDF stays reachable

	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer sc.Close()

	if err := sc.Raw(func(dc any) error {
		c := dc.(*sqlite.Conn)
		return c.RegisterWindowFunction("rsum", 1,
			func() sqlite.WindowAccumulator { return &runningSum{} }, true)
	}); err != nil {
		log.Fatal(err)
	}

	if _, err := sc.ExecContext(ctx,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, v REAL)`); err != nil {
		log.Fatal(err)
	}
	for i, v := range []float64{10, 20, 30, 40, 50} {
		if _, err := sc.ExecContext(ctx,
			`INSERT INTO t (id, v) VALUES (?, ?)`, i+1, v); err != nil {
			log.Fatal(err)
		}
	}

	rows, err := sc.QueryContext(ctx, `
        SELECT id, v,
            rsum(v) OVER (
                ORDER BY id
                ROWS BETWEEN 1 PRECEDING AND CURRENT ROW
            ) AS moving_sum_2
        FROM t ORDER BY id`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	fmt.Println("id  v   moving_sum_2")
	for rows.Next() {
		var id int64
		var v, sum float64
		if err := rows.Scan(&id, &v, &sum); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%d   %3.0f  %3.0f\n", id, v, sum)
	}
}
