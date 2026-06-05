package sqlite

import (
	"database/sql"
	"testing"
)

// TestSQLiteVersion is a smoke test that the gorm sub-package's
// DriverName resolves through database/sql to a working SQLite
// connection. Asserts the version string isn't empty — a typical
// `3.x.y` string today.
func TestSQLiteVersion(t *testing.T) {
	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		t.Fatalf("sql.Open(%q): %v", DriverName, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var version string
	if err := db.QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatalf("QueryRow(sqlite_version): %v", err)
	}
	if version == "" {
		t.Error("sqlite_version() returned empty string")
	}
}
