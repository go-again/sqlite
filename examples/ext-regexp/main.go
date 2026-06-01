// ext-regexp: REGEXP operator + regexp_* SQL functions via blank-import
// auto-registration. Run with: just example ext-regexp
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "github.com/go-again/sqlite"
	_ "github.com/go-again/sqlite/ext/regexp/auto"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE users(name TEXT, email TEXT);
		INSERT INTO users VALUES ('Alice', 'alice@example.com'),
		    ('bob', 'bob@invalid'),
		    ('Carol', 'carol@example.com'),
		    ('dave', 'dave@test.io');`); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Users with capitalized names (REGEXP operator):")
	rows, _ := db.QueryContext(ctx, `SELECT name FROM users WHERE name REGEXP ?`, `^[A-Z]`)
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		fmt.Printf("  %s\n", n)
	}
	rows.Close()

	fmt.Println("\nDomains extracted via regexp_substr:")
	rows, _ = db.QueryContext(ctx, `SELECT regexp_substr(email, '@([^.]+\.[a-z]+)', 1, 1, 1) FROM users`)
	for rows.Next() {
		var d sql.NullString
		_ = rows.Scan(&d)
		fmt.Printf("  %s\n", d.String)
	}
	rows.Close()

	fmt.Println("\nMasked emails via regexp_replace:")
	rows, _ = db.QueryContext(ctx, `SELECT regexp_replace(email, '(\w+)@(.+)', '***@$2') FROM users`)
	for rows.Next() {
		var e string
		_ = rows.Scan(&e)
		fmt.Printf("  %s\n", e)
	}
	rows.Close()
}
