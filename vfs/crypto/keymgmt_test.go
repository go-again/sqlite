package crypto

import (
	"errors"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
)

func makeRecipientsDB(t *testing.T, name string, opts Options) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	db, err := Open(sqlite.Config{Path: path}, opts)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRewrapRecipients re-seals the keyslot to a new recipient set without
// re-encrypting: an added recipient gains access, a removed one loses it.
func TestRewrapRecipients(t *testing.T) {
	aliceR, aliceID, _ := keyring.GenerateX25519()
	bobR, bobID, _ := keyring.GenerateX25519()
	carolR, carolID, _ := keyring.GenerateX25519()
	path := makeRecipientsDB(t, "rw.db", Options{Recipients: []keyring.Recipient{aliceR, bobR}})

	if err := Rewrap(path, aliceID, nil, []keyring.Recipient{aliceR, carolR}); err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	for name, id := range map[string]keyring.Identity{"alice": aliceID, "carol": carolID} {
		if n, err := openCount(t, path, Options{Identities: []keyring.Identity{id}}); err != nil || n != 1 {
			t.Fatalf("open as %s after rewrap: count=%d err=%v", name, n, err)
		}
	}
	if n, err := openCount(t, path, Options{Identities: []keyring.Identity{bobID}}); err == nil {
		t.Errorf("removed bob still opens (count=%d)", n)
	}
}

// TestRewrapMasterGated: only a master may change a master-protected membership.
func TestRewrapMasterGated(t *testing.T) {
	masterR, masterID, _ := keyring.GenerateMaster()
	memberR, memberID, _ := keyring.GenerateX25519()
	newR, newID, _ := keyring.GenerateX25519()
	path := makeRecipientsDB(t, "rwm.db", Options{
		Masters:    []keyring.MasterRecipient{masterR},
		SignWith:   masterID,
		Recipients: []keyring.Recipient{memberR},
	})

	// A non-master recipient cannot administer.
	if err := Rewrap(path, memberID, []keyring.MasterRecipient{masterR}, []keyring.Recipient{memberR, newR}); !errors.Is(err, ErrNotMaster) {
		t.Fatalf("member rewrap = %v, want ErrNotMaster", err)
	}
	// The master adds newR.
	if err := Rewrap(path, masterID, []keyring.MasterRecipient{masterR}, []keyring.Recipient{memberR, newR}); err != nil {
		t.Fatalf("master rewrap: %v", err)
	}
	if n, err := openCount(t, path, Options{Identities: []keyring.Identity{newID}, Masters: []keyring.MasterRecipient{masterR}}); err != nil || n != 1 {
		t.Fatalf("added recipient open: count=%d err=%v", n, err)
	}
}

// TestRewrapNotRecipients: a raw-key database has no keyslot sidecar to rewrap.
func TestRewrapNotRecipients(t *testing.T) {
	aliceR, aliceID, _ := keyring.GenerateX25519()
	path := makeRecipientsDB(t, "raw.db", Options{Key: make([]byte, 32)})
	if err := Rewrap(path, aliceID, nil, []keyring.Recipient{aliceR}); err == nil {
		t.Error("rewrap of a raw-key database: want error")
	}
}
