package vault

import (
	"errors"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
)

// TestMembers: a master enumerates a recipients database's membership; a read-only
// member cannot; the set tracks a Rewrap with no drift; a raw-key database has none.
func TestMembers(t *testing.T) {
	admin, adminID, err := keyring.GenerateMaster()
	if err != nil {
		t.Fatal(err)
	}
	member, memberID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "members.db")
	db, err := Open(sqlite.Config{Path: path}, Options{
		Masters:    []keyring.MasterRecipient{admin},
		SignWith:   adminID,
		Recipients: []keyring.Recipient{member},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE t(v)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// The admin enumerates the full set: itself (master) and the member.
	ms, err := Members(path, adminID)
	if err != nil {
		t.Fatalf("Members as admin: %v", err)
	}
	roles := map[string]int{}
	for _, m := range ms {
		roles[m.Role]++
	}
	if roles["master"] != 1 || roles["member"] != 1 || len(ms) != 2 {
		t.Fatalf("membership = %+v (roles %v), want one master + one member", ms, roles)
	}

	// A read-only member is not a master identity and cannot enumerate.
	if _, err := Members(path, memberID); !errors.Is(err, keyring.ErrNotMaster) {
		t.Fatalf("Members as member = %v, want ErrNotMaster", err)
	}

	// Adding a member via Rewrap is reflected with no drift.
	member2, _, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	if err := Rewrap(path, adminID, nil, keyring.Membership{
		Masters: []keyring.MasterRecipient{admin},
		Members: []keyring.Recipient{member, member2},
	}); err != nil {
		t.Fatalf("Rewrap: %v", err)
	}
	ms2, err := Members(path, adminID)
	if err != nil {
		t.Fatalf("Members after Rewrap: %v", err)
	}
	if len(ms2) != 3 {
		t.Fatalf("membership after Rewrap = %d entries, want 3: %+v", len(ms2), ms2)
	}
}

// TestMembersRawKeyDatabase: a raw-key (Options.Key) database has no keyslot and no
// membership record.
func TestMembersRawKeyDatabase(t *testing.T) {
	_, adminID, err := keyring.GenerateMaster()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rawkey.db")
	db, err := Open(sqlite.Config{Path: path}, Options{Key: randKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE t(v)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Members(path, adminID); err == nil {
		t.Fatal("Members on a raw-key database succeeded; want an error")
	}
}
