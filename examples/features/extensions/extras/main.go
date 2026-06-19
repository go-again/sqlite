// ext-extras: the new scalar extension families in one place — exact
// decimal arithmetic, fixed-point money, Go-time helpers, and dynamic
// SQL via eval(). Each is blank-imported through its /auto package so it
// registers on every connection.
//
// Run with:
//
//	just example extras
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	_ "gosqlite.org/ext/decimal/auto"
	_ "gosqlite.org/ext/eval/auto"
	_ "gosqlite.org/ext/money/auto"
	_ "gosqlite.org/ext/time/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	q := func(sql string) string {
		var s string
		if err := db.QueryRowContext(ctx, sql).Scan(&s); err != nil {
			log.Fatalf("%s: %v", sql, err)
		}
		return s
	}

	// Exact base-10: binary floats would give 0.30000000000000004.
	fmt.Println("decimal_add('0.1','0.2')   =", q(`SELECT decimal_add('0.1','0.2')`))
	fmt.Println("decimal_mul('19.99','3')   =", q(`SELECT decimal_mul('19.99','3')`))

	// Money: fixed two decimals + thousands separators.
	fmt.Println("money_mul('19.99','1000')  =", q(`SELECT money_mul('19.99','1000')`))
	fmt.Println("money_format('1234567.5')  =", q(`SELECT money_format('1234567.5')`))

	// Time: duration arithmetic and field extraction over Go's time.
	fmt.Println("time_add(.., '48h')        =", q(`SELECT time_add('2026-01-01T00:00:00Z','48h')`))
	fmt.Println("time_part(.., 'weekday')   =", q(`SELECT time_part('2026-06-14T00:00:00Z','weekday')`))

	// eval: run SQL produced by SQL (trusted input only).
	_, _ = db.ExecContext(ctx, `CREATE TABLE nums(v); INSERT INTO nums VALUES (10),(20),(30)`)
	fmt.Println("eval('SELECT sum(v)...')   =", q(`SELECT eval('SELECT sum(v) FROM nums')`))
	fmt.Println("eval(list with separator)  =", q(`SELECT eval('SELECT v FROM nums ORDER BY v', ' + ')`))
}
