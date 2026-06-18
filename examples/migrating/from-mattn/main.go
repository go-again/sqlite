// from-mattn: shows that swapping the blank import from
// "github.com/mattn/go-sqlite3" to "github.com/go-again/sqlite" needs no other
// code changes. The driver name "sqlite3" still works, all the DSN _* flags
// you used with mattn still work, and the SQLiteDriver / SQLiteConn / Error
// types are exposed under the same names via type aliases.
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"

	// The only line that needs to change in a mattn-based project is this
	// import path. Everything else — sql.Open("sqlite3", ...), DSN flags,
	// SQLiteDriver literals — stays the same.
	sqlite "github.com/go-again/sqlite"
)

func main() {
	// 1. Open with mattn-style _* DSN flags. The translator turns these into
	//    the corresponding PRAGMA executions transparently.
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// 2. Use the mattn ConnectHook pattern via &SQLiteDriver{...}. Tests
	//    and prod code that register custom UDFs this way work as-is.
	sql.Register("mattn-style", &sqlite.SQLiteDriver{
		ConnectHook: func(conn *sqlite.SQLiteConn) error {
			return conn.RegisterFunc("double", func(x int64) int64 { return x * 2 }, true)
		},
	})
	customDB, err := sql.Open("mattn-style", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer customDB.Close()

	var doubled int64
	if err := customDB.QueryRow("SELECT double(21)").Scan(&doubled); err != nil {
		log.Fatal(err)
	}
	fmt.Println("UDF result:", doubled) // -> 42

	// 3. Constraint errors expose mattn-compatible Code / ExtendedCode and
	//    work with errors.Is against the ErrConstraint family.
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		log.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t (id) VALUES (1)`); err != nil {
		log.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO t (id) VALUES (1)`)
	if errors.Is(err, sqlite.ErrConstraintPrimaryKey) {
		fmt.Println("dup key, as expected")
	}

	var se *sqlite.Error
	if errors.As(err, &se) {
		fmt.Printf("primary code=%d, extended code=%d\n", se.Code(), se.ExtendedCode())
	}
}
