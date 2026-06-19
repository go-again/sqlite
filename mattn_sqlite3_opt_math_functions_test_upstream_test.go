// Vendored from github.com/mattn/go-sqlite3 v1.14.x to validate
// mattn-compatibility against this fork. Build tag `mattn_upstream`
// keeps it out of the default `go test ./...`. See
// dev/upstream/mattn.md.

//go:build mattn_upstream

package sqlite

import (
	"database/sql"
	"testing"
)

func TestMathFunctions(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal("Failed to open database:", err)
	}
	defer db.Close()

	queries := []string{
		`SELECT acos(1)`,
		`SELECT log(10, 100)`,
		`SELECT power(2, 2)`,
	}

	for _, query := range queries {
		var result float64
		if err := db.QueryRow(query).Scan(&result); err != nil {
			t.Errorf("invoking math function query %q: %v", query, err)
		}
	}
}
