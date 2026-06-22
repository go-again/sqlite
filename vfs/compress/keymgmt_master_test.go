package compress

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
)

// makeMasterDB creates a master-protected database (Masters + SignWith) with the
// given members, populates it, closes it, and returns the path.
func makeMasterDB(t *testing.T, masters []keyring.MasterRecipient, signWith keyring.MasterIdentity, members []keyring.Recipient) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master.dbz")
	db, err := Open(sqlite.Config{Path: path}, Options{Masters: masters, SignWith: signWith, Recipients: members})
	if err != nil {
		t.Fatalf("create master db: %v", err)
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

// openCountTrusting opens with an identity while pinning trusted masters, so the
// keyslot signature is verified.
func openCountTrusting(path string, id keyring.Identity, trusted []keyring.MasterRecipient) (int, error) {
	db, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{id}, Masters: trusted})
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT count(*) FROM t`).Scan(&n)
	return n, err
}

// TestMasterModel: a member reads (pinning the master); pinning the wrong master
// is rejected; a member cannot administer; a master adds a member; a master
// rekey removes a member cryptographically.
func TestMasterModel(t *testing.T) {
	m1R, m1ID, _ := keyring.GenerateMaster()
	_, m2ID, _ := keyring.GenerateMaster() // a different, untrusted master
	m2R, _, _ := keyring.GenerateMaster()
	aliceR, aliceID, _ := keyring.GenerateX25519()

	path := makeMasterDB(t, []keyring.MasterRecipient{m1R}, m1ID, []keyring.Recipient{aliceR})
	trustM1 := []keyring.MasterRecipient{m1R}

	// The member reads when pinning the real master.
	if n, err := openCountTrusting(path, aliceID, trustM1); err != nil || n != 50 {
		t.Fatalf("member read pinning m1: count=%d err=%v", n, err)
	}
	// Pinning the wrong master rejects the keyslot (signed by m1, not m2).
	if _, err := openCountTrusting(path, aliceID, []keyring.MasterRecipient{m2R}); err == nil {
		t.Fatal("pinning the wrong master: want rejection")
	}
	// A member cannot administer.
	bobR, bobID, _ := keyring.GenerateX25519()
	if err := Rewrap(path, aliceID, nil, keyring.Membership{Masters: trustM1, Members: []keyring.Recipient{aliceR, bobR}}); !errors.Is(err, ErrNotMaster) {
		t.Fatalf("member Rewrap = %v, want ErrNotMaster", err)
	}
	// The master adds bob.
	if err := Rewrap(path, m1ID, nil, keyring.Membership{Masters: trustM1, Members: []keyring.Recipient{aliceR, bobR}}); err != nil {
		t.Fatalf("master Rewrap: %v", err)
	}
	if n, err := openCountTrusting(path, bobID, trustM1); err != nil || n != 50 {
		t.Fatalf("bob read after master add: count=%d err=%v", n, err)
	}
	// The master rekeys to drop alice (members = {bob}).
	if err := Rekey(path, m1ID, nil, keyring.Membership{Masters: trustM1, Members: []keyring.Recipient{bobR}}); err != nil {
		t.Fatalf("master Rekey: %v", err)
	}
	if _, err := openCountTrusting(path, aliceID, trustM1); err == nil {
		t.Fatal("dropped alice still opens after rekey")
	}
	if n, err := openCountTrusting(path, bobID, trustM1); err != nil || n != 50 {
		t.Fatalf("bob read after rekey: count=%d err=%v", n, err)
	}
	_ = m2ID
}

// TestRemoveMaster: with two masters, one can rekey the other out — afterward the
// removed master can neither read nor administer.
func TestRemoveMaster(t *testing.T) {
	m1R, m1ID, _ := keyring.GenerateMaster()
	m2R, m2ID, _ := keyring.GenerateMaster()
	aliceR, _, _ := keyring.GenerateX25519()

	path := makeMasterDB(t, []keyring.MasterRecipient{m1R, m2R}, m1ID, []keyring.Recipient{aliceR})

	// m2 rekeys, keeping only itself as master.
	if err := Rekey(path, m2ID, nil, keyring.Membership{Masters: []keyring.MasterRecipient{m2R}, Members: []keyring.Recipient{aliceR}}); err != nil {
		t.Fatalf("m2 rekey: %v", err)
	}
	// m1 (removed) can no longer read ...
	if _, err := openCountTrusting(path, m1ID, []keyring.MasterRecipient{m2R}); err == nil {
		t.Fatal("removed master m1 still reads")
	}
	// ... nor administer (it cannot even recover the data key).
	if err := Rewrap(path, m1ID, nil, keyring.Membership{Masters: []keyring.MasterRecipient{m1R}, Members: []keyring.Recipient{aliceR}}); err == nil {
		t.Fatal("removed master m1 still administers")
	}
	// m2 remains in control.
	if n, err := openCountTrusting(path, m2ID, []keyring.MasterRecipient{m2R}); err != nil || n != 50 {
		t.Fatalf("m2 read after removing m1: count=%d err=%v", n, err)
	}
}
