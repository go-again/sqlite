package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
)

// TestKeyslotBannerVisible confirms a recipients-encrypted container identifies
// itself on disk: the keyslot block carries the "gosqlite.org/vault/v1" banner
// (right beside the age envelope it wraps, which carries age's own marker), and
// the superblock carries the VAULTv01 magic.
func TestKeyslotBannerVisible(t *testing.T) {
	aR, _, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	bR, _, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "branded.db")
	db, err := Open(sqlite.Config{Path: path}, Options{Recipients: []keyring.Recipient{aR, bR}})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE t(v)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(keyslotBanner)) {
		t.Error("keyslot banner (gosqlite.org/vault/v1) not found in the on-disk container")
	}
	if !bytes.Contains(raw, []byte(superblockMagic)) {
		t.Errorf("superblock magic %q not found in the on-disk container", superblockMagic)
	}
}
