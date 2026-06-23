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
	before := fileSize(t, path)

	if err := Compact(sqlite.Config{Path: path}, Options{}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	after := fileSize(t, path)
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
	key := randKey(t)
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

// TestReservedPathRefusesOpen: while an offline op holds a path reservation, Open
// is refused (the registry TOCTOU guard) and is allowed again once released.
func TestReservedPathRefusesOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "res.db")
	db, err := Open(sqlite.Config{Path: path}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reservePath(abs) {
		t.Fatal("reservePath on a closed database failed")
	}
	if d, err := Open(sqlite.Config{Path: path}, Options{}); err == nil {
		_ = d.Close()
		releasePath(abs)
		t.Fatal("Open succeeded while the path was reserved; want a refusal")
	}
	releasePath(abs)

	d2, err := Open(sqlite.Config{Path: path}, Options{}) // allowed again
	if err != nil {
		t.Fatalf("Open after release: %v", err)
	}
	_ = d2.Close()
}

// TestCompactEncryptedRequiresKey: compacting an encrypted database with only
// Identities (forgetting Recipients) must be REFUSED, not silently rewritten as
// plaintext. The original file is left intact.
func TestCompactEncryptedRequiresKey(t *testing.T) {
	alice, aliceID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	bob, bobID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rk.db")
	marker := []byte("REQUIRES-KEY-marker-must-stay-encrypted")

	db, err := Open(sqlite.Config{Path: path}, Options{Recipients: []keyring.Recipient{alice, bob}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v BLOB)`); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, marker); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	// Only Identities (no Recipients): must error before touching the file.
	if err := Compact(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{aliceID}}); err == nil {
		t.Fatal("Compact with only Identities on an encrypted database succeeded; want a refusal")
	}

	// The original is untouched and still encrypted: no plaintext marker on disk,
	// and a recipient still reads it.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, marker) {
		t.Fatal("plaintext marker on disk after a refused compact — database was decrypted")
	}
	rdb, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{bobID}})
	if err != nil {
		t.Fatalf("original database damaged after a refused compact: %v", err)
	}
	defer rdb.Close()
	if n := rowCount(t, rdb); n != 20 {
		t.Fatalf("row count after refused compact = %d, want 20", n)
	}
}

// TestCompactPreservesGeometry: Compact takes the page/block geometry from the
// source, so compacting a non-default-page-size database with default Options does
// not corrupt it.
func TestCompactPreservesGeometry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geo.db")
	db, err := Open(sqlite.Config{Path: path}, Options{PageSize: 8192, BlockSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		if _, err := db.Exec(`INSERT INTO t(id, v) VALUES(?, ?)`, i, "row"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM t WHERE id >= 20; VACUUM`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// Compact with DEFAULT options (no PageSize): geometry must come from the source.
	if err := Compact(sqlite.Config{Path: path}, Options{}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	db2, err := Open(sqlite.Config{Path: path}, Options{PageSize: 8192, BlockSize: 1024})
	if err != nil {
		t.Fatalf("reopen compacted non-default-geometry db: %v", err)
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

// TestCompactWriterSigned: a writer-signed database compacts (with the writer
// identity) and reopens verifiably for a read-only recipient.
func TestCompactWriterSigned(t *testing.T) {
	admin, adminID, err := keyring.GenerateMaster()
	if err != nil {
		t.Fatal(err)
	}
	reader, readerID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ws.db")
	create := Options{
		Masters:    []keyring.MasterRecipient{admin},
		SignWith:   adminID,
		Writers:    []keyring.WriterRecipient{admin},
		WriteAs:    adminID,
		Recipients: []keyring.Recipient{reader},
	}
	db, err := Open(sqlite.Config{Path: path}, create)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	// Compacting needs an identity to READ the source plus the full creds to re-seal
	// and re-sign the copy (the admin is a recipient, so adminID reads it).
	compactOpts := create
	compactOpts.Identities = []keyring.Identity{adminID}
	if err := Compact(sqlite.Config{Path: path}, compactOpts); err != nil {
		t.Fatalf("Compact writer-signed: %v", err)
	}

	rdb, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{readerID}, Masters: []keyring.MasterRecipient{admin}})
	if err != nil {
		t.Fatalf("reopen compacted writer-signed db as reader: %v", err)
	}
	defer rdb.Close()
	if n := rowCount(t, rdb); n != 30 {
		t.Fatalf("row count after compact = %d, want 30", n)
	}
}

// TestCompactContinuesGenerationForAnchor: Compact advances the replay anchor (it
// does not reset the generation), so the compacted file opens under the anchor and
// a pre-compaction image is still rejected as a rollback.
func TestCompactContinuesGenerationForAnchor(t *testing.T) {
	key := randKey(t)
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
