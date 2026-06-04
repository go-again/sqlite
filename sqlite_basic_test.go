package sqlite

import (
	"database/sql"
	"testing"
)

// TestDriverNamesRegistered ensures both "sqlite" and "sqlite3" point at this
// driver, so both modernc-style and mattn-style sql.Open calls work.
func TestDriverNamesRegistered(t *testing.T) {
	for _, name := range []string{DriverName, DriverNameSQLite3} {
		t.Run(name, func(t *testing.T) {
			db, err := sql.Open(name, ":memory:")
			if err != nil {
				t.Fatalf("open(%q): %v", name, err)
			}
			defer db.Close()

			var got int
			if err := db.QueryRow("SELECT 42").Scan(&got); err != nil {
				t.Fatalf("query: %v", err)
			}
			if got != 42 {
				t.Fatalf("got %d, want 42", got)
			}
		})
	}
}

// TestBasicCRUD exercises the common path: CREATE, INSERT, SELECT, with a
// parameterized query and result scanning.
func TestBasicCRUD(t *testing.T) {
	db, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO t (name) VALUES (?), (?), (?)`, "alice", "bob", "carol")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 3 {
		t.Fatalf("RowsAffected: got %d, want 3", n)
	}

	rows, err := db.Query(`SELECT name FROM t WHERE id >= ? ORDER BY id`, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "bob" || got[1] != "carol" {
		t.Fatalf("got %v, want [bob carol]", got)
	}
}
