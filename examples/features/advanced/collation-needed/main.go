// collation-needed: define collations on demand. AnyCollationNeeded lets a
// connection open / query a schema that references collations this process does
// not implement (treating them as byte-wise), and CollationNeeded installs a
// real comparator lazily the first time one is referenced.
//
// Run with:
//
//	just example collation-needed
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	sqlite "gosqlite.org"
)

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer sc.Close()

	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x TEXT)`); err != nil {
		log.Fatal(err)
	}
	for _, v := range []string{"banana", "Apple", "cherry"} {
		if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (?)`, v); err != nil {
			log.Fatal(err)
		}
	}

	// Referencing an unknown collation fails...
	if _, err := sc.QueryContext(ctx, `SELECT x FROM t ORDER BY x COLLATE de_DE`); err != nil {
		fmt.Printf("  before: ORDER BY ... COLLATE de_DE → %v\n", err)
	}

	// ...install a real case-insensitive comparator on demand for "de_DE".
	if err := sc.Raw(func(dc any) error {
		return dc.(*sqlite.Conn).CollationNeeded(func(conn *sqlite.Conn, name string) {
			if name == "de_DE" {
				_ = conn.RegisterCollation(name, func(a, b string) int {
					return strings.Compare(strings.ToLower(a), strings.ToLower(b))
				})
			}
		})
	}); err != nil {
		log.Fatal(err)
	}

	fmt.Println("\n  after CollationNeeded (case-insensitive de_DE):")
	printOrder(ctx, sc, "de_DE")

	// AnyCollationNeeded would satisfy *every* unknown collation as byte-wise
	// (uppercase before lowercase) — handy for opening a foreign schema:
	if err := sc.Raw(func(dc any) error { return dc.(*sqlite.Conn).AnyCollationNeeded() }); err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n  AnyCollationNeeded byte-wise for an arbitrary unknown collation 'xx_YY':")
	printOrder(ctx, sc, "xx_YY")
}

func printOrder(ctx context.Context, sc *sql.Conn, collation string) {
	rows, err := sc.QueryContext(ctx, `SELECT x FROM t ORDER BY x COLLATE `+collation)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			log.Fatal(err)
		}
		out = append(out, s)
	}
	fmt.Printf("    %v\n", out)
}
