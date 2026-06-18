// udf-context: the FunctionContext substrate for custom SQL functions —
// per-argument auxiliary-data caching (compile a regexp once for a constant
// pattern, reuse it for every row) and result/argument subtypes.
//
// Run with:
//
//	just example udf-context
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"regexp"
	"sync/atomic"

	sqlite "github.com/go-again/sqlite"
)

func main() {
	var compiles atomic.Int64

	// regex_match(pattern, text): when `pattern` is a constant across rows,
	// SQLite preserves the compiled *regexp.Regexp via SetAuxData, so it is
	// compiled once instead of once per row.
	if err := sqlite.RegisterFunction("regex_match", &sqlite.FunctionImpl{
		NArgs:     2,
		Innocuous: true, // usable from views/triggers under defensive mode
		Scalar: func(ctx *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			re, ok := ctx.GetAuxData(0)
			if !ok {
				compiled, err := regexp.Compile(args[0].(string))
				if err != nil {
					return nil, err
				}
				compiles.Add(1)
				ctx.SetAuxData(0, compiled) // cache for the next row
				re = compiled
			}
			return re.(*regexp.Regexp).MatchString(args[1].(string)), nil
		},
	}); err != nil {
		log.Fatal(err)
	}

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE emails(addr TEXT)`); err != nil {
		log.Fatal(err)
	}
	for _, a := range []string{"alice@example.com", "bob@invalid", "carol@example.org", "not-an-email"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO emails VALUES (?)`, a); err != nil {
			log.Fatal(err)
		}
	}

	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM emails WHERE regex_match('^[^@]+@[^@]+\.[a-z]+$', addr)`).Scan(&n); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("  %d of 4 addresses look valid\n", n)
	fmt.Printf("  regexp compiled %d time(s) for 4 rows (aux-data cache reused the constant pattern)\n",
		compiles.Load())
}
