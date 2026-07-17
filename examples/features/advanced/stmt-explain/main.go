// stmt-explain: inspect a query's plan without re-preparing, then run it — one
// prepared statement. (*Stmt).Explain flips a statement to EXPLAIN QUERY PLAN (or
// EXPLAIN) mode at runtime and back; bound parameters carry over, so you can read
// the plan and then execute the exact same statement. Reach the driver *Stmt via
// (*sql.Conn).Raw.
//
// Run with:
//
//	just example stmt-explain
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log"

	sqlite "gosqlite.org"
)

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	// A table + an index, so the plan has something to choose.
	if _, err := db.ExecContext(ctx, `CREATE TABLE users(id INTEGER PRIMARY KEY, city TEXT, age INT)`); err != nil {
		log.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE INDEX users_city ON users(city)`); err != nil {
		log.Fatal(err)
	}
	for i, c := range []string{"paris", "london", "paris", "berlin", "paris"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO users(city, age) VALUES (?, ?)`, c, 20+i*5); err != nil {
			log.Fatal(err)
		}
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	const q = `SELECT id, age FROM users WHERE city = ? ORDER BY age`
	if err := conn.Raw(func(dc any) error {
		c := dc.(*sqlite.Conn)
		ds, err := c.Prepare(q)
		if err != nil {
			return err
		}
		defer ds.Close()
		st := ds.(*sqlite.Stmt)

		// 1) Inspect the plan — no re-prepare, the bound param carries over.
		if err := st.Explain(sqlite.ExplainQueryPlan); err != nil {
			return err
		}
		if st.IsExplain() != sqlite.ExplainQueryPlan {
			return errors.New("IsExplain should report ExplainQueryPlan")
		}
		fmt.Println("EXPLAIN QUERY PLAN:")
		if err := query(ctx, ds, "paris", func(row []driver.Value) {
			fmt.Printf("  %v\n", row[len(row)-1]) // the EQP "detail" column
		}); err != nil {
			return err
		}

		// 2) Flip the same statement back to normal and run it for real.
		if err := st.Explain(sqlite.ExplainOff); err != nil {
			return err
		}
		if st.IsExplain() != sqlite.ExplainOff {
			return errors.New("IsExplain should report ExplainOff after reset")
		}
		fmt.Println("Results (city = 'paris'):")
		n := 0
		if err := query(ctx, ds, "paris", func(row []driver.Value) {
			n++
			fmt.Printf("  id=%v age=%v\n", row[0], row[1])
		}); err != nil {
			return err
		}
		if n != 3 {
			return fmt.Errorf("got %d paris rows, want 3", n)
		}
		fmt.Printf("  %d rows — the plan and the run came from one prepared statement\n", n)
		return nil
	}); err != nil {
		log.Fatal(err)
	}
}

// query runs a driver statement with one positional argument, invoking fn per row.
func query(ctx context.Context, ds driver.Stmt, arg string, fn func([]driver.Value)) error {
	rows, err := ds.(driver.StmtQueryContext).QueryContext(ctx, []driver.NamedValue{{Ordinal: 1, Value: arg}})
	if err != nil {
		return err
	}
	defer rows.Close()
	dest := make([]driver.Value, len(rows.Columns()))
	for {
		if err := rows.Next(dest); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		fn(dest)
	}
}
