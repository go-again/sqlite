package crypto

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
)

// openCount opens the database with the given options and returns the row count,
// or the error surfaced at open or first access.
func openCount(t *testing.T, path string, opts Options) (int, error) {
	t.Helper()
	db, err := Open(sqlite.Config{Path: path}, opts)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT count(*) FROM t`).Scan(&n)
	return n, err
}

// TestRecipientsSidecar drives the multi-recipient crypto VFS: a database
// encrypted to two recipients writes its data key into a detached keyslot
// sidecar, either recipient opens it with their own identity, an unlisted
// identity cannot, and the plaintext never reaches the database file.
func TestRecipientsSidecar(t *testing.T) {
	const marker = "CRYPTO_RECIPIENTS_SECRET_42"
	aliceR, aliceID, _ := keyring.GenerateX25519()
	bobR, bobID, _ := keyring.GenerateX25519()
	path := filepath.Join(t.TempDir(), "rcpt.db")

	db, err := Open(sqlite.Config{Path: path}, Options{Recipients: []keyring.Recipient{aliceR, bobR}})
	if err != nil {
		t.Fatalf("create recipients db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, marker+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path + keyslotSuffix); err != nil {
		t.Fatalf("keyslot sidecar missing: %v", err)
	}
	if raw, err := os.ReadFile(path); err == nil && bytes.Contains(raw, []byte(marker)) {
		t.Error("plaintext marker found in the encrypted database file")
	}

	for name, id := range map[string]keyring.Identity{"alice": aliceID, "bob": bobID} {
		if n, err := openCount(t, path, Options{Identities: []keyring.Identity{id}}); err != nil || n != 30 {
			t.Fatalf("open as %s: count=%d err=%v", name, n, err)
		}
	}

	// An unlisted identity cannot open it.
	_, eveID, _ := keyring.GenerateX25519()
	if n, err := openCount(t, path, Options{Identities: []keyring.Identity{eveID}}); err == nil {
		t.Errorf("unlisted identity opened the database (count=%d)", n)
	}

	// Key and Recipients are mutually exclusive.
	if _, _, err := New(Options{Key: make([]byte, 32), Recipients: []keyring.Recipient{aliceR}}); err == nil {
		t.Error("New with both Key and Recipients: want error")
	}
}

// TestMissingSidecarRefused: a recipients database whose keyslot sidecar is lost
// must NOT silently mint a fresh key over the existing (now-unreadable) data — it
// must refuse, whether reopened with the original Recipients or with an Identity.
func TestMissingSidecarRefused(t *testing.T) {
	aliceR, aliceID, _ := keyring.GenerateX25519()
	path := filepath.Join(t.TempDir(), "lost.db")

	db, err := Open(sqlite.Config{Path: path}, Options{Recipients: []keyring.Recipient{aliceR}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if err := os.Remove(path + keyslotSuffix); err != nil {
		t.Fatal(err)
	}

	// Reopen with the create options over the existing DB: must refuse, not mint.
	if n, err := openCount(t, path, Options{Recipients: []keyring.Recipient{aliceR}}); err == nil {
		t.Errorf("missing sidecar over existing data minted a fresh key (count=%d)", n)
	}
	// And the refusal must not have written a new sidecar.
	if _, err := os.Stat(path + keyslotSuffix); err == nil {
		t.Error("a new keyslot sidecar was created over existing data")
	}
	// Reopen with only an identity: also a clean error.
	if n, err := openCount(t, path, Options{Identities: []keyring.Identity{aliceID}}); err == nil {
		t.Errorf("missing sidecar opened with an identity (count=%d)", n)
	}
}

// TestCorruptSidecarRejected: a garbage or oversized keyslot file fails cleanly
// rather than parsing wrong or OOMing.
func TestCorruptSidecarRejected(t *testing.T) {
	aliceR, aliceID, _ := keyring.GenerateX25519()
	path := filepath.Join(t.TempDir(), "corrupt.db")
	db, err := Open(sqlite.Config{Path: path}, Options{Recipients: []keyring.Recipient{aliceR}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if err := os.WriteFile(path+keyslotSuffix, []byte("not a keyslot blob"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := openCount(t, path, Options{Identities: []keyring.Identity{aliceID}}); err == nil {
		t.Errorf("corrupt sidecar opened (count=%d)", n)
	}
	if err := os.WriteFile(path+keyslotSuffix, make([]byte, maxKeyslotBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := openCount(t, path, Options{Identities: []keyring.Identity{aliceID}}); err == nil {
		t.Errorf("oversized sidecar opened (count=%d)", n)
	}
}

// TestMasterSidecar pins an administrator on the crypto keyslot: a member opens
// it when pinning the real master, and pinning the wrong master is rejected.
func TestMasterSidecar(t *testing.T) {
	masterR, masterID, _ := keyring.GenerateMaster()
	wrongR, _, _ := keyring.GenerateMaster()
	memberR, memberID, _ := keyring.GenerateX25519()
	path := filepath.Join(t.TempDir(), "master.db")

	db, err := Open(sqlite.Config{Path: path}, Options{
		Masters:    []keyring.MasterRecipient{masterR},
		SignWith:   masterID,
		Recipients: []keyring.Recipient{memberR},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if n, err := openCount(t, path, Options{Identities: []keyring.Identity{memberID}, Masters: []keyring.MasterRecipient{masterR}}); err != nil || n != 1 {
		t.Fatalf("member read pinning the master: count=%d err=%v", n, err)
	}
	if n, err := openCount(t, path, Options{Identities: []keyring.Identity{memberID}, Masters: []keyring.MasterRecipient{wrongR}}); err == nil {
		t.Errorf("pinning the wrong master opened the database (count=%d)", n)
	}
}
