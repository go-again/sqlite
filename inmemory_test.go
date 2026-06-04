package sqlite_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	sqlite "github.com/go-again/sqlite"
)

// TestInMemoryConstantIsValidDSN: the bare constant must work with
// every entry point — database/sql, the typed sqlite.Open, and the
// helper sqlite.OpenInMemory().
func TestInMemoryConstantIsValidDSN(t *testing.T) {
	if sqlite.InMemory != ":memory:" {
		t.Errorf("sqlite.InMemory=%q, want \":memory:\"", sqlite.InMemory)
	}

	db, err := sql.Open(sqlite.DriverName, sqlite.InMemory)
	if err != nil {
		t.Fatalf("sql.Open(sqlite.InMemory): %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT 1`).Scan(&n); err != nil {
		t.Errorf("ping via sql.Open: %v", err)
	}
}

func TestOpenInMemory(t *testing.T) {
	db, err := sqlite.OpenInMemory()
	if err != nil {
		t.Fatalf("sqlite.OpenInMemory: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(v INT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES (1), (2), (3)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("count=%d, want 3", n)
	}
}

// TestOpenWAL: production preset applies. WAL mode propagates as a
// DB-file attribute so we can verify it via PRAGMA on any conn.
func TestOpenWAL(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.OpenWAL(filepath.Join(dir, "wal.db"))
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	defer db.Close()
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode=%q, want \"wal\"", mode)
	}
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys=%d, want 1", fk)
	}
}

// TestOpenReadOnly: refuses to create a missing file (matches
// mode=ro semantics) and refuses writes once attached to an
// existing file.
func TestOpenReadOnly(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.db")
	if _, err := sqlite.OpenReadOnly(missing); err == nil {
		t.Error("OpenReadOnly on missing file: want error, got nil")
	}

	// Seed an existing file via OpenWAL, then reopen RO.
	existing := filepath.Join(dir, "seed.db")
	seed, err := sqlite.OpenWAL(existing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`CREATE TABLE t(v INT); INSERT INTO t VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	seed.Close()

	ro, err := sqlite.OpenReadOnly(existing)
	if err != nil {
		t.Fatalf("OpenReadOnly on existing file: %v", err)
	}
	defer ro.Close()
	var v int
	if err := ro.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 42 {
		t.Errorf("v=%d, want 42", v)
	}
	if _, err := ro.Exec(`INSERT INTO t VALUES (99)`); err == nil {
		t.Error("write to read-only DB: want error, got nil")
	}
}

// TestOpenShared: two distinct *sql.DB handles opened against the
// same name see the same rows — the test that pins the
// shared-cache contract OpenShared exists for.
func TestOpenShared(t *testing.T) {
	const name = "shortcuts-shared-test"

	a, err := sqlite.OpenShared(name)
	if err != nil {
		t.Fatalf("OpenShared (a): %v", err)
	}
	defer a.Close()
	if _, err := a.Exec(`CREATE TABLE t(v INT); INSERT INTO t VALUES (1), (2)`); err != nil {
		t.Fatal(err)
	}

	b, err := sqlite.OpenShared(name)
	if err != nil {
		t.Fatalf("OpenShared (b): %v", err)
	}
	defer b.Close()
	var n int
	if err := b.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("b sees no rows: %v", err)
	}
	if n != 2 {
		t.Errorf("b count=%d, want 2 (shared cache should expose a's writes)", n)
	}
}
