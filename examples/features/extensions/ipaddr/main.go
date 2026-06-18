// ext-ipaddr: IP / CIDR SQL helpers. Run with: just example ipaddr
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "github.com/go-again/sqlite"
	_ "github.com/go-again/sqlite/ext/ipaddr/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE events(id INTEGER PRIMARY KEY, src TEXT);
		INSERT INTO events(src) VALUES
		    ('10.0.0.5'), ('10.1.2.3'), ('192.168.1.42'),
		    ('203.0.113.7'), ('2001:db8::1'), ('::1');`); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Events inside 10.0.0.0/8 (ipcontains):")
	rows, _ := db.QueryContext(ctx, `SELECT src FROM events WHERE ipcontains('10.0.0.0/8', src)`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		fmt.Printf("  %s\n", s)
	}
	rows.Close()

	fmt.Println("\nFamily classification:")
	rows, _ = db.QueryContext(ctx, `SELECT src, ipfamily(src) FROM events`)
	for rows.Next() {
		var src string
		var fam int64
		_ = rows.Scan(&src, &fam)
		fmt.Printf("  %-20s → IPv%d\n", src, fam)
	}
	rows.Close()

	fmt.Println("\nNetwork normalization:")
	for _, p := range []string{"10.1.2.3/8", "192.168.1.42/24", "2001:db8:1::1/32"} {
		var net string
		_ = db.QueryRowContext(ctx, `SELECT ipnetwork(?)`, p).Scan(&net)
		fmt.Printf("  ipnetwork(%-20s) = %s\n", p, net)
	}
}
