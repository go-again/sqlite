// ext-array: bind a Go slice as a SQL table-valued function via the
// array extension. Shows two binding styles — the transparent
// sqlite.Pointer(slice) form (preferred) and the explicit Bind/Release
// pair (useful for long-lived bindings).
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

	// Blank-import auto-registers the array module on every connection
	// the driver opens.
	_ "github.com/go-again/sqlite/ext/array/auto"
)

func main() {
	db, err := sqlite.Open(sqlite.Config{Path: ":memory:"})
	if err != nil {
		log.Fatalf("Open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Pin a single connection because :memory: databases are per-conn —
	// the table we'll create below lives on this conn only.
	sc, err := db.Conn(ctx)
	if err != nil {
		log.Fatalf("Conn: %v", err)
	}
	defer sc.Close()

	// --- 1. Transparent path via sqlite.Pointer ---
	//
	// SQLite drives the binding lifetime through a destructor
	// trampoline — no explicit Release call needed.
	values := []any{int64(42), "hello", nil, 3.14, true}

	fmt.Println("Transparent binding via sqlite.Pointer:")
	fmt.Println("rowid | type    | value")
	fmt.Println("------+---------+------")
	rows, err := sc.QueryContext(ctx,
		`SELECT rowid, typeof(value), CAST(value AS TEXT) FROM array(?)`,
		sqlite.Pointer(values))
	if err != nil {
		log.Fatalf("Query: %v", err)
	}
	for rows.Next() {
		var rid int64
		var ty, val sql.NullString
		if err := rows.Scan(&rid, &ty, &val); err != nil {
			log.Fatalf("Scan: %v", err)
		}
		fmt.Printf("%-5d | %-7s | %s\n", rid, ty.String, val.String)
	}
	rows.Close()

	// --- 2. Compose with regular SQL — JOIN against a normal table ---
	if _, err := sc.ExecContext(ctx, `CREATE TABLE labels(id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		log.Fatalf("CREATE: %v", err)
	}
	if _, err := sc.ExecContext(ctx,
		`INSERT INTO labels(id, name) VALUES (1, 'first'), (2, 'second'), (3, 'third')`); err != nil {
		log.Fatalf("INSERT: %v", err)
	}
	fmt.Println()
	fmt.Println("JOIN labels against array(?) for selective lookup:")
	rows2, err := sc.QueryContext(ctx, `
		SELECT labels.id, labels.name
		FROM labels
		JOIN array(?) AS a ON a.value = labels.id
		ORDER BY labels.id`, sqlite.Pointer([]int{1, 3}))
	if err != nil {
		log.Fatalf("JOIN Query: %v", err)
	}
	for rows2.Next() {
		var id int64
		var name string
		if err := rows2.Scan(&id, &name); err != nil {
			log.Fatalf("Scan: %v", err)
		}
		fmt.Printf("  id=%d name=%q\n", id, name)
	}
	rows2.Close()

	// --- 3. Explicit Bind / Release escape hatch ---
	//
	// When you want a single binding reused across multiple queries
	// (or want an int64 sentinel you can pass through other code
	// paths), array.Bind returns a token + release closure.
	var conn *sqlite.Conn
	_ = sc.Raw(func(driverConn any) error {
		conn = driverConn.(*sqlite.Conn)
		return nil
	})

	token, release := array.Bind(conn, []string{"alpha", "beta", "gamma"})
	defer release()

	fmt.Println()
	fmt.Println("Explicit Bind/Release with reused token:")
	for range 2 {
		rows3, err := sc.QueryContext(ctx,
			`SELECT value FROM array(?) ORDER BY value`, token)
		if err != nil {
			log.Fatalf("reused Query: %v", err)
		}
		for rows3.Next() {
			var v string
			_ = rows3.Scan(&v)
			fmt.Printf("  %s\n", v)
		}
		rows3.Close()
	}
}
