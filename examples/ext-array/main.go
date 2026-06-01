// ext-array: bind a Go slice as a SQL table-valued function via the array
// extension. Demonstrates both registration styles — explicit per-conn
// Register, and blank-import auto.
//
// Run with:
//
//	just example ext-array
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/array"

	// Demonstrate the blank-import auto-registration path. With this
	// import, every connection the driver opens has the array module
	// available — no explicit Register call needed.
	_ "github.com/go-again/sqlite/ext/array/auto"
)

func main() {
	db, err := sqlite.Open(sqlite.Config{Path: ":memory:"})
	if err != nil {
		log.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Grab the underlying *sqlite.Conn so we can Bind a Go slice.
	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatalf("Conn: %v", err)
	}
	defer sc.Close()
	var conn *sqlite.Conn
	if err := sc.Raw(func(driverConn any) error {
		conn = driverConn.(*sqlite.Conn)
		return nil
	}); err != nil {
		log.Fatalf("Raw: %v", err)
	}

	// Bind a heterogeneous slice: each element becomes a SQL row in the
	// natural SQLite type (NULL → NULL, int → INTEGER, string → TEXT,
	// float → REAL).
	values := []any{int64(42), "hello", nil, 3.14, true}
	token, release := array.Bind(conn, values)
	defer release()

	rows, err := sc.QueryContext(ctx,
		`SELECT rowid, typeof(value), CAST(value AS TEXT) FROM array(?)`,
		token)
	if err != nil {
		log.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	fmt.Println("rowid | type    | value")
	fmt.Println("------+---------+------")
	for rows.Next() {
		var rid int64
		var ty, val sql.NullString
		if err := rows.Scan(&rid, &ty, &val); err != nil {
			log.Fatalf("Scan: %v", err)
		}
		fmt.Printf("%-5d | %-7s | %s\n", rid, ty.String, val.String)
	}

	// Compose with regular SQL: join the array against a normal table.
	// :memory: databases are per-connection, so everything stays on the
	// same *sql.Conn we pinned above.
	if _, err := sc.ExecContext(ctx, `CREATE TABLE labels(id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		log.Fatalf("CREATE: %v", err)
	}
	if _, err := sc.ExecContext(ctx,
		`INSERT INTO labels(id, name) VALUES (1, 'first'), (2, 'second'), (3, 'third')`); err != nil {
		log.Fatalf("INSERT: %v", err)
	}
	idsToken, idsRelease := array.Bind(conn, []int{1, 3})
	defer idsRelease()
	fmt.Println()
	fmt.Println("JOIN labels against array(?) for selective lookup:")
	rows2, err := sc.QueryContext(ctx, `
		SELECT labels.id, labels.name
		FROM labels
		JOIN array(?) AS a ON a.value = labels.id
		ORDER BY labels.id`, idsToken)
	if err != nil {
		log.Fatalf("JOIN Query: %v", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var id int64
		var name string
		if err := rows2.Scan(&id, &name); err != nil {
			log.Fatalf("Scan: %v", err)
		}
		fmt.Printf("  id=%d name=%q\n", id, name)
	}
}
