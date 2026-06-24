package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
)

// TestSnapshotReseal: Snapshot writes an encrypted, compressed backup to a new
// path, re-sealed to a DIFFERENT recipient; the new recipient opens it, the old one
// does not, no plaintext lands on disk, and the source is untouched.
func TestSnapshotReseal(t *testing.T) {
	aR, aID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	bR, bID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "backup.db")
	marker := []byte("snapshot-secret-marker-stays-encrypted")

	db, err := Open(sqlite.Config{Path: src}, Options{Recipients: []keyring.Recipient{aR}, Level: CompressionDefault})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE t(v BLOB)`)
	for range 50 {
		mustExec(t, db, `INSERT INTO t VALUES(?)`, marker)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Read with A, re-seal the backup to B (and recompress harder).
	if err := Snapshot(dst, src,
		Options{Identities: []keyring.Identity{aID}},
		Options{Recipients: []keyring.Recipient{bR}, Level: CompressionBest}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// B opens the backup and reads every row.
	bdb, err := Open(sqlite.Config{Path: dst}, Options{Identities: []keyring.Identity{bID}})
	if err != nil {
		t.Fatalf("open backup as B: %v", err)
	}
	if n := rowCount(t, bdb); n != 50 {
		t.Fatalf("backup row count = %d, want 50", n)
	}
	if err := bdb.Close(); err != nil {
		t.Fatal(err)
	}

	// A was NOT re-sealed into the backup, so it cannot open it.
	if adb, err := Open(sqlite.Config{Path: dst}, Options{Identities: []keyring.Identity{aID}}); err == nil {
		_ = adb.Close()
		t.Fatal("the old recipient opened a backup re-sealed to a different recipient")
	}

	// No plaintext marker on the encrypted backup.
	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, marker) {
		t.Fatal("plaintext marker found in the encrypted backup")
	}

	// The source is untouched: A still opens it with all rows.
	adb, err := Open(sqlite.Config{Path: src}, Options{Identities: []keyring.Identity{aID}})
	if err != nil {
		t.Fatalf("source damaged by Snapshot: %v", err)
	}
	if n := rowCount(t, adb); n != 50 {
		t.Fatalf("source row count after Snapshot = %d, want 50", n)
	}
	if err := adb.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestSnapshotRefusesPlaintext: Snapshot of an encrypted source with no re-seal
// (no Key or Recipients in writeOpts) refuses rather than write a plaintext copy,
// and creates no destination file.
func TestSnapshotRefusesPlaintext(t *testing.T) {
	aR, aID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "leak.db")

	db, err := Open(sqlite.Config{Path: src}, Options{Recipients: []keyring.Recipient{aR}})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE t(v)`)
	mustExec(t, db, `INSERT INTO t VALUES(1)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Snapshot(dst, src, Options{Identities: []keyring.Identity{aID}}, Options{}); err == nil {
		t.Fatal("Snapshot of an encrypted source to a plaintext destination succeeded; want a refusal")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("Snapshot created the destination despite refusing")
	}
}

// TestSnapshotFreshGeneration: a Snapshot of an authenticated source whose
// generation has advanced past 2 starts the backup at a FRESH generation (2),
// independent of the source — the property that justifies Snapshot existing apart
// from an in-place rewrite. Proven by reading the floor the backup records in its
// own anchor. A src==dst call is refused.
func TestSnapshotFreshGeneration(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "backup.db")
	key := randKey(t)

	db, err := Open(sqlite.Config{Path: src}, Options{Key: key, Authenticate: true})
	if err != nil {
		t.Fatal(err)
	}
	// Several separate transactions advance the source's committed generation well
	// past 2 (each Exec commits a container generation).
	mustExec(t, db, `CREATE TABLE t(v)`)
	for i := range 5 {
		mustExec(t, db, `INSERT INTO t VALUES(?)`, i)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	floor := FileAnchor(filepath.Join(dir, "backup.floor"))
	if err := Snapshot(dst, src,
		Options{Key: key, Authenticate: true},
		Options{Key: key, Authenticate: true, Anchor: floor}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// The backup recorded its own fresh generation (2), not the source's continued
	// generation (which is > 2 after the transactions above).
	gen, err := floor.LoadGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if gen != 2 {
		t.Fatalf("backup anchor floor = %d, want 2 (a fresh generation independent of src)", gen)
	}

	// The backup opens under its own anchor and reads back intact.
	bdb, err := Open(sqlite.Config{Path: dst}, Options{Key: key, Authenticate: true, Anchor: floor})
	if err != nil {
		t.Fatalf("open backup under its anchor: %v", err)
	}
	if n := rowCount(t, bdb); n != 5 {
		t.Fatalf("backup row count = %d, want 5", n)
	}
	if err := bdb.Close(); err != nil {
		t.Fatal(err)
	}

	// Snapshot refuses to target the source path.
	if err := Snapshot(src, src, Options{Key: key, Authenticate: true}, Options{Key: key, Authenticate: true}); err == nil {
		t.Fatal("Snapshot(src, src) succeeded; want a refusal")
	}
}

// TestSnapshotPlain: a plain (unencrypted) Snapshot copies a container to a new
// path that reads back intact, leaving the source in place.
func TestSnapshotPlain(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "copy.db")

	db, err := Open(sqlite.Config{Path: src}, Options{Level: CompressionDefault})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE t(v)`)
	for i := range 40 {
		mustExec(t, db, `INSERT INTO t VALUES(?)`, i)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Snapshot(dst, src, Options{}, Options{Level: CompressionBest}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	cdb, err := Open(sqlite.Config{Path: dst}, Options{})
	if err != nil {
		t.Fatalf("open copy: %v", err)
	}
	if n := rowCount(t, cdb); n != 40 {
		t.Fatalf("copy row count = %d, want 40", n)
	}
	if err := cdb.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source missing after Snapshot: %v", err)
	}
}
