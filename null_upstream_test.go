// Vendored from modernc.org/sqlite (see go.mod for the pin) to run against the fork in this
// repository. Build tag `modernc_upstream` keeps it out of the default
// `go test ./...` so the long-running upstream suite stays opt-in.
// See dev/upstream/modernc.md for the reproduction recipe.

//go:build modernc_upstream

package sqlite

import (
	"database/sql"
	"testing"
)

func TestNullBinding(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Errorf("cannot open: %v", err)
		return
	}
	_, err = db.Exec(`
	CREATE TABLE table1 (field1 varchar NULL);
	INSERT INTO table1 (field1) VALUES (?);
	`, sql.NullString{})
	if err != nil {
		t.Errorf("Error binding null: %v", err)
	}
	err = db.Close()
	if err != nil {
		t.Errorf("cannot close: %v", err)
		return
	}
}
