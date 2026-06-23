package vault

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
)

// churn fills a database with incompressible rows, deletes almost all of them, and
// VACUUMs — leaving a small logical database inside a container whose physical file
// has plateaued large (freed blocks are reused, not returned to the OS).
func churn(t *testing.T, db *sqlite.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, blob BLOB)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO t(id, blob) VALUES(?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	blob := make([]byte, 1024)
	for i := range 3000 {
		if _, err := rand.Read(blob); err != nil {
			t.Fatal(err)
		}
		if _, err := stmt.Exec(i, blob); err != nil {
			t.Fatal(err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM t WHERE id >= 30`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		t.Fatal(err)
	}
}

func size(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func rowCount(t *testing.T, db *sqlite.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCompactShrinks: a churned plain container shrinks after Compact and still
// reads back intact.
func TestCompactShrinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "churn.db")
	db, err := Open(sqlite.Config{Path: path}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	churn(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := size(t, path)

	if err := Compact(sqlite.Config{Path: path}, Options{}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after := size(t, path)
	if after >= before {
		t.Fatalf("Compact did not shrink the file: before=%d after=%d", before, after)
	}

	db2, err := Open(sqlite.Config{Path: path}, Options{})
	if err != nil {
		t.Fatalf("reopen after compact: %v", err)
	}
	defer db2.Close()
	if n := rowCount(t, db2); n != 30 {
		t.Fatalf("row count after compact = %d, want 30", n)
	}
	var ic string
	if err := db2.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check = (%q, %v)", ic, err)
	}
}

// TestCompactEncryptedAuthenticated: Compact preserves encryption + authenticated
// mode (the compacted file reopens, verifies, and holds no plaintext).
func TestCompactEncryptedAuthenticated(t *testing.T) {
	key := anchorKey(t)
	path := filepath.Join(t.TempDir(), "enc.db")
	opts := Options{Key: key, Authenticate: true, Level: CompressionDefault}

	db, err := Open(sqlite.Config{Path: path}, opts)
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte("PLAINTEXT-MARKER-should-never-hit-disk")
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, blob BLOB)`); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		if _, err := db.Exec(`INSERT INTO t(id, blob) VALUES(?, ?)`, i, marker); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM t WHERE id >= 20`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Compact(sqlite.Config{Path: path}, opts); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, marker) {
		t.Fatal("plaintext marker present in the compacted file")
	}

	db2, err := Open(sqlite.Config{Path: path}, opts)
	if err != nil {
		t.Fatalf("reopen compacted authenticated db: %v", err)
	}
	defer db2.Close()
	if n := rowCount(t, db2); n != 20 {
		t.Fatalf("row count after compact = %d, want 20", n)
	}
	var ic string
	if err := db2.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check = (%q, %v)", ic, err)
	}
}

// TestCompactRecipients: compacting a multi-recipient database round-trips — the
// fresh keyslot is re-wrapped to the same recipients, who can still open it.
func TestCompactRecipients(t *testing.T) {
	alice, aliceID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	bob, bobID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rcpt.db")
	create := Options{Recipients: []keyring.Recipient{alice, bob}, Level: CompressionDefault}

	db, err := Open(sqlite.Config{Path: path}, create)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM t WHERE v >= 5; VACUUM`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// Compact: read as Alice, re-wrap to both recipients.
	compactOpts := Options{
		Recipients: []keyring.Recipient{alice, bob},
		Identities: []keyring.Identity{aliceID},
		Level:      CompressionDefault,
	}
	if err := Compact(sqlite.Config{Path: path}, compactOpts); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Bob still opens the compacted file with his own identity.
	rdb, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{bobID}})
	if err != nil {
		t.Fatalf("reopen compacted recipients db as bob: %v", err)
	}
	defer rdb.Close()
	if n := rowCount(t, rdb); n != 5 {
		t.Fatalf("row count after compact = %d, want 5", n)
	}
}

// TestCompactContinuesGenerationForAnchor: Compact advances the replay anchor (it
// does not reset the generation), so the compacted file opens under the anchor and
// a pre-compaction image is still rejected as a rollback.
func TestCompactContinuesGenerationForAnchor(t *testing.T) {
	key := anchorKey(t)
	anchor := &memAnchor{}
	path := filepath.Join(t.TempDir(), "anch.db")
	opts := Options{Key: key, Authenticate: true, Anchor: anchor}

	db, err := Open(sqlite.Config{Path: path}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	preCompact, err := os.ReadFile(path) // a validly-signed image at the pre-compaction generation
	if err != nil {
		t.Fatal(err)
	}
	floorBefore := anchor.gen

	if err := Compact(sqlite.Config{Path: path}, opts); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if anchor.gen <= floorBefore {
		t.Fatalf("Compact did not advance the anchor floor (%d <= %d)", anchor.gen, floorBefore)
	}

	// The compacted file opens cleanly under the (advanced) anchor.
	db2, err := Open(sqlite.Config{Path: path}, opts)
	if err != nil {
		t.Fatalf("reopen compacted db under anchor: %v", err)
	}
	if n := rowCount(t, db2); n != 20 {
		t.Fatalf("row count = %d, want 20", n)
	}
	_ = db2.Close()

	// Restoring the pre-compaction image is now a rollback below the floor.
	if err := os.WriteFile(path, preCompact, 0o600); err != nil {
		t.Fatal(err)
	}
	if rdb, err := Open(sqlite.Config{Path: path}, opts); err == nil {
		var n int
		if qerr := rdb.QueryRow(`SELECT count(*) FROM t`).Scan(&n); qerr == nil {
			t.Error("pre-compaction image opened after compaction; want rollback rejection")
		}
		_ = rdb.Close()
	}
}
