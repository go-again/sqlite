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

// seedBigDelete creates a database with a large table and a small one, then deletes
// the large one WITHOUT vacuuming — so page_count stays high (the freed pages are on
// the freelist). It returns the path and the post-delete file size.
func seedBigDelete(t *testing.T, path string, opts Options) int64 {
	t.Helper()
	db, err := Open(sqlite.Config{Path: path}, opts)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE big(id INTEGER PRIMARY KEY, v BLOB)`)
	mustExec(t, db, `CREATE TABLE small(id INTEGER PRIMARY KEY, v BLOB)`)
	blob := make([]byte, 64*1024)
	_, _ = rand.Read(blob)
	for i := range 256 { // ~16 MiB
		mustExec(t, db, `INSERT INTO big(id, v) VALUES(?, ?)`, i, blob)
	}
	for i := range 50 {
		mustExec(t, db, `INSERT INTO small(id, v) VALUES(?, ?)`, i, blob[:400])
	}
	mustExec(t, db, `DELETE FROM big`) // no incremental_vacuum
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return fileSize(t, path)
}

// TestCompactLogicalOLive: CompactLogical reclaims a large deletion WITHOUT a prior
// vacuum (which physical Compact requires), shrinking the file to ~live size in one
// pass, for plaintext and raw-key encrypted databases.
func TestCompactLogicalOLive(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts Options
	}{
		{"plaintext", Options{PageSize: 8192}},
		{"rawkey", Options{PageSize: 8192, Key: bytes.Repeat([]byte{4}, 32)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.db")
			before := seedBigDelete(t, path, tc.opts)

			if err := CompactLogical(sqlite.Config{Path: path}, tc.opts); err != nil {
				t.Fatalf("CompactLogical: %v", err)
			}
			after := fileSize(t, path)
			t.Logf("%s: %d KiB -> %d KiB (no pre-vacuum)", tc.name, before/1024, after/1024)
			if after >= before/4 {
				t.Fatalf("CompactLogical did not reclaim O-live: before %d, after %d", before, after)
			}

			// Reopens, data intact, still writable.
			db, err := Open(sqlite.Config{Path: path}, tc.opts)
			if err != nil {
				t.Fatalf("reopen after CompactLogical: %v", err)
			}
			defer db.Close()
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM small`).Scan(&n); err != nil || n != 50 {
				t.Fatalf("small rows after CompactLogical = (%d,%v), want 50", n, err)
			}
			if n := func() int { var c int; db.QueryRow(`SELECT count(*) FROM big`).Scan(&c); return c }(); n != 0 {
				t.Fatalf("big rows after CompactLogical = %d, want 0", n)
			}
			mustExec(t, db, `INSERT INTO small(id, v) VALUES(99999, ?)`, []byte("post"))
		})
	}
}

// TestCompactLogicalPreservesMembership: CompactLogical of a recipients image with
// only a read identity rebuilds O-live AND keeps the membership — every recipient
// still opens the result, a non-member does not, no plaintext leaks.
func TestCompactLogicalPreservesMembership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	alice, aliceID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	bob, bobID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	_, strangerID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}

	marker := []byte("LOGICAL-preserve-stays-encrypted")
	before := seedBigDelete(t, path, Options{PageSize: 8192, Recipients: []keyring.Recipient{alice, bob}})
	// Tag a small-table row with a marker we can grep for on disk.
	db, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{aliceID}})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO small(id, v) VALUES(424242, ?)`, marker)
	_ = db.Close()

	// Identity-only: preserves the keyslot + data key.
	if err := CompactLogical(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{aliceID}}); err != nil {
		t.Fatalf("CompactLogical -identity: %v", err)
	}
	after := fileSize(t, path)
	t.Logf("recipients: %d KiB -> %d KiB", before/1024, after/1024)
	if after >= before/4 {
		t.Fatalf("CompactLogical recipients did not reclaim O-live: before %d, after %d", before, after)
	}

	// Still encrypted (no plaintext marker), and both recipients open; a non-member does not.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, marker) {
		t.Fatal("plaintext marker on disk after CompactLogical — database was decrypted")
	}
	for name, id := range map[string]keyring.Identity{"alice": aliceID, "bob": bobID} {
		d, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{id}})
		if err != nil {
			t.Fatalf("reopen as %s: %v", name, err)
		}
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM small`).Scan(&n); err != nil || n != 51 {
			t.Fatalf("%s sees %d small rows, want 51", name, n)
		}
		_ = d.Close()
	}
	if d, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{strangerID}}); err == nil {
		_ = d.Close()
		t.Fatal("a non-member opened the compacted image; membership leaked")
	}
}

// TestCompactLogicalRefusesAnchor: an anchored database must use Compact (the
// logical rebuild starts a fresh generation, which an anchor floor would reject).
func TestCompactLogicalRefusesAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.db")
	key := bytes.Repeat([]byte{8}, 32)
	anchor := FileAnchor(filepath.Join(dir, "floor"))
	db, err := Open(sqlite.Config{Path: path}, Options{Key: key, Authenticate: true, Anchor: anchor})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE t(v)`)
	_ = db.Close()
	if err := CompactLogical(sqlite.Config{Path: path}, Options{Key: key, Authenticate: true, Anchor: anchor}); err == nil {
		t.Fatal("CompactLogical on an anchored database succeeded; want a refusal")
	}
}
