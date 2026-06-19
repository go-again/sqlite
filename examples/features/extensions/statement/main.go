// ext-statement: define a parametrized view via the `statement` vtab.
// The view's bound parameters become HIDDEN columns that callers
// constrain in their WHERE clause.
//
// Run with:
//
//	just example statement
package main

import (
	"context"
	"fmt"
	"log"

	sqlite "gosqlite.org"
	_ "gosqlite.org/ext/statement/auto"
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
		`CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`); err != nil {
		log.Fatalf("CREATE: %v", err)
	}
	for _, r := range [][]any{
		{"alice", 30}, {"bob", 17}, {"carol", 45}, {"dave", 12},
	} {
		if _, err := sc.ExecContext(ctx,
			`INSERT INTO users(name, age) VALUES (?, ?)`, r...); err != nil {
			log.Fatalf("INSERT: %v", err)
		}
	}

	// Define a parametrized view: anyone whose age >= some bound.
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE adults USING statement(
		    'SELECT name, age FROM users WHERE age >= ? ORDER BY age')`); err != nil {
		log.Fatalf("CREATE VTAB: %v", err)
	}

	// Anonymous params become "?1", "?2", ... HIDDEN columns.
	fmt.Println("Adults (age >= 18):")
	rows, err := sc.QueryContext(ctx,
		`SELECT name, age FROM adults WHERE "?1" = 18`)
	if err != nil {
		log.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var age int
		if err := rows.Scan(&name, &age); err != nil {
			log.Fatalf("Scan: %v", err)
		}
		fmt.Printf("  %s (%d)\n", name, age)
	}

	// Named params land as their own HIDDEN column.
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE byPattern USING statement(
		    'SELECT name FROM users WHERE name LIKE :pat ORDER BY name')`); err != nil {
		log.Fatalf("CREATE VTAB2: %v", err)
	}
	fmt.Println()
	fmt.Println("Names matching 'a%':")
	rows2, err := sc.QueryContext(ctx,
		`SELECT name FROM byPattern WHERE pat = 'a%'`)
	if err != nil {
		log.Fatalf("Query2: %v", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var name string
		_ = rows2.Scan(&name)
		fmt.Printf("  %s\n", name)
	}
}
