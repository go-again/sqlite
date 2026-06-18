// database-sql: the modern, idiomatic foundation — plain database/sql on
// the "sqlite" driver, with no legacy DSN flags. Raw SQL here is not
// "old"; it's the standard library way and the base every other example
// builds on. Reach for sqlite.Config (see ../config) when you need
// structured setup (pragmas, encryption, pool tuning), and for gorm or
// the typed vec/fts handles when you want higher-level ergonomics.
//
// Run with:
//
//	just example database-sql
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "github.com/go-again/sqlite" // registers the "sqlite" (and "sqlite3") driver
)

type author struct {
	id   int64
	name string
}

func main() {
	ctx := context.Background()

	// "sqlite" is the modern driver name. A shared in-memory database
	// needs a single connection so every statement sees the same data.
	db, err := sql.Open("sqlite", "file:app.db?mode=memory&cache=shared")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
	`); err != nil {
		log.Fatal(err)
	}

	// Parameterized writes — never string-concatenate user input.
	res, err := db.ExecContext(ctx,
		`INSERT INTO authors (name) VALUES (?), (?), (?)`, "Ada", "Grace", "Edsger")
	if err != nil {
		log.Fatal(err)
	}
	n, _ := res.RowsAffected()
	fmt.Printf("inserted %d authors\n", n)

	// Single-row read.
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM authors`).Scan(&count); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("authors in table: %d\n", count)

	// Multi-row read with the standard rows.Next/Scan/Err loop.
	rows, err := db.QueryContext(ctx, `SELECT id, name FROM authors ORDER BY name`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a author
		if err := rows.Scan(&a.id, &a.name); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  [%d] %s\n", a.id, a.name)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}

	// Transactions commit on success, roll back on error.
	if err := withTx(ctx, db); err != nil {
		log.Fatal(err)
	}
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM authors`).Scan(&count)
	fmt.Printf("authors after the transaction: %d\n", count)
}

func withTx(ctx context.Context, db *sql.DB) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `INSERT INTO authors (name) VALUES (?)`, "Alan"); err != nil {
		return err
	}
	return tx.Commit()
}
