// from-modernc: shows that swapping the blank import from
// "modernc.org/sqlite" to "github.com/go-again/sqlite" needs no other code
// changes. The driver name "sqlite" still works, all the modernc DSN flags
// (_pragma, _time_format, _time_integer_format, _timezone, _txlock,
// _inttotime, _texttotime, vfs=...) still work, and the modernc-style
// registration helpers (RegisterScalarFunction, RegisterDeterministicScalarFunction,
// RegisterCollationUtf8, RegisterConnectionHook) are exposed under the same
// names.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"strings"

	// The only line that needs to change in a modernc-based project is this
	// import path. Everything else — sql.Open("sqlite", ...), DSN flags,
	// modernc registration helpers — stays the same.
	sqlite "github.com/go-again/sqlite"
)

func main() {
	// 1. Register a modernc-style scalar function at the driver level. This
	//    is the same call signature as modernc's RegisterScalarFunction and
	//    affects every connection opened after this point.
	if err := sqlite.RegisterDeterministicScalarFunction(
		"upper_go",
		1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			s, _ := args[0].(string)
			return strings.ToUpper(s), nil
		},
	); err != nil {
		log.Fatal(err)
	}

	// 2. Register a UTF-8 collation, modernc-style.
	if err := sqlite.RegisterCollationUtf8("ci", strings.Compare); err != nil {
		log.Fatal(err)
	}

	// 3. Install a connection hook. Modernc-style hooks receive the new
	//    connection as ExecQuerierContext and the DSN string.
	sqlite.RegisterConnectionHook(func(c sqlite.ExecQuerierContext, dsn string) error {
		// Demonstrate the hook fires: tag this conn via a session pragma.
		_, _ = c.ExecContext(context.Background(), "PRAGMA application_id = 0xC0DE", nil)
		return nil
	})

	// 4. Open with the modernc driver name "sqlite" (mattn's "sqlite3" also
	//    works — both names point at the same singleton driver).
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)&_time_format=sqlite")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var got string
	if err := db.QueryRow("SELECT upper_go('hello world')").Scan(&got); err != nil {
		log.Fatal(err)
	}
	fmt.Println("UDF result:", got) // -> HELLO WORLD

	// 5. Connection hook ran — application_id is set.
	var appID int
	if err := db.QueryRow("PRAGMA application_id").Scan(&appID); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("application_id = 0x%X (set by ConnectionHook)\n", appID)

	// 6. Limit helper (modernc-shape): wraps conn.Raw + sqlite3_limit.
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	current, err := sqlite.Limit(conn, sqlite.SQLITE_LIMIT_LENGTH, -1)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("SQLITE_LIMIT_LENGTH default:", current)
}
