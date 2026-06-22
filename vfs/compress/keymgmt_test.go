package compress

import (
	"path/filepath"
	"strconv"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
)

const kmMarker = "KEYMGMT_SECRET_VALUE"

// makeRecipientsDB creates a recipients-encrypted database at a fresh path,
// populates it, closes it, and returns the path.
func makeRecipientsDB(t *testing.T, recipients ...keyring.Recipient) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "km.dbz")
	db, err := Open(sqlite.Config{Path: path}, Options{Recipients: recipients})
	if err != nil {
		t.Fatalf("create recipients db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, kmMarker+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// openCountAs opens the database with a single identity and returns the row
// count (or the error from the first access, where a non-matching identity
// surfaces).
func openCountAs(path string, id keyring.Identity) (int, error) {
	db, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{id}})
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT count(*) FROM t`).Scan(&n)
	return n, err
}

// TestRewrap re-wraps the data key to a new recipient set without re-encrypting:
// an added recipient gains access, a removed one loses it, and the data is
// intact under the unchanged key.
func TestRewrap(t *testing.T) {
	aliceR, aliceID, _ := keyring.GenerateX25519()
	bobR, bobID, _ := keyring.GenerateX25519()
	carolR, carolID, _ := keyring.GenerateX25519()

	path := makeRecipientsDB(t, aliceR, bobR)

	// Remove bob, add carol (recovering the key with alice's identity).
	if err := Rewrap(path, aliceID, nil, keyring.Membership{Members: []keyring.Recipient{aliceR, carolR}}); err != nil {
		t.Fatalf("rewrap: %v", err)
	}

	// alice (kept) and carol (added) can open; the data is intact.
	for name, id := range map[string]keyring.Identity{"alice": aliceID, "carol": carolID} {
		n, err := openCountAs(path, id)
		if err != nil || n != 50 {
			t.Fatalf("open as %s after rewrap: count=%d err=%v", name, n, err)
		}
	}

	// bob (removed) can no longer open.
	if n, err := openCountAs(path, bobID); err == nil {
		t.Fatalf("open as removed bob: want error, got count=%d", n)
	}
}

// TestRekey re-encrypts under a fresh key: the kept recipient still reads the
// data, and the removed recipient is locked out cryptographically.
func TestRekey(t *testing.T) {
	aliceR, aliceID, _ := keyring.GenerateX25519()
	bobR, bobID, _ := keyring.GenerateX25519()

	path := makeRecipientsDB(t, aliceR, bobR)

	// Re-encrypt, granting only alice.
	if err := Rekey(path, bobID, nil, keyring.Membership{Members: []keyring.Recipient{aliceR}}); err != nil {
		t.Fatalf("rekey: %v", err)
	}

	// alice reads the re-encrypted data.
	if n, err := openCountAs(path, aliceID); err != nil || n != 50 {
		t.Fatalf("open as alice after rekey: count=%d err=%v", n, err)
	}
	// bob is locked out even though he could decrypt before.
	if n, err := openCountAs(path, bobID); err == nil {
		t.Fatalf("open as revoked bob after rekey: want error, got count=%d", n)
	}
}

// TestKeyMgmtErrors covers the guards: empty recipient set, a raw-key database,
// and a database that is currently open.
func TestKeyMgmtErrors(t *testing.T) {
	aliceR, aliceID, _ := keyring.GenerateX25519()

	t.Run("no recipients", func(t *testing.T) {
		path := makeRecipientsDB(t, aliceR)
		if err := Rewrap(path, aliceID, nil, keyring.Membership{}); err == nil {
			t.Fatal("rewrap with no recipients: want error")
		}
		if err := Rekey(path, aliceID, nil, keyring.Membership{}); err == nil {
			t.Fatal("rekey with no recipients: want error")
		}
	})

	t.Run("raw-key database", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "raw.dbz")
		db, err := Open(sqlite.Config{Path: path}, Options{Key: make([]byte, 32)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		if err := Rewrap(path, aliceID, nil, keyring.Membership{Members: []keyring.Recipient{aliceR}}); err == nil {
			t.Fatal("rewrap of a raw-key database: want error")
		}
	})

	t.Run("open database", func(t *testing.T) {
		path := makeRecipientsDB(t, aliceR)
		db, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{aliceID}})
		if err != nil {
			t.Fatal(err)
		}
		// Force the container open (and registered) before the at-rest call.
		if _, err := db.Exec(`SELECT 1`); err != nil {
			t.Fatal(err)
		}
		if err := Rewrap(path, aliceID, nil, keyring.Membership{Members: []keyring.Recipient{aliceR}}); err == nil {
			t.Error("rewrap of an open database: want error")
		}
		_ = db.Close()
	})
}
