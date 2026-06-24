package keyring

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// TestMembersRoundTrip: a master enumerates the full membership it sealed — the
// masters, writers, and members, each with its public key and (for an SSH key) its
// comment as a label.
func TestMembersRoundTrip(t *testing.T) {
	masterR, masterID, err := GenerateMaster()
	if err != nil {
		t.Fatal(err)
	}
	writerR, _, err := GenerateMaster()
	if err != nil {
		t.Fatal(err)
	}
	x25519R, _, err := GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	// An SSH member built from a line carrying a comment, so the label is exercised.
	sshAuth, _ := sshKeypair(t)
	sshLine := append(bytes.TrimSpace(sshAuth), []byte(" alice@laptop")...)
	sshR, err := SSHRecipient(sshLine)
	if err != nil {
		t.Fatal(err)
	}

	blob, err := SealKeyslot(freshDEK(t), Membership{
		Masters: []MasterRecipient{masterR},
		Writers: []WriterRecipient{writerR},
		Members: []Recipient{x25519R, sshR},
	}, masterID)
	if err != nil {
		t.Fatal(err)
	}

	members, err := Members(blob, masterID)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 4 {
		t.Fatalf("got %d members, want 4: %+v", len(members), members)
	}

	roles := map[string]int{}
	var ssh *Member
	for i := range members {
		roles[members[i].Role]++
		if members[i].Label == "alice@laptop" {
			ssh = &members[i]
		}
	}
	if roles["master"] != 1 || roles["writer"] != 1 || roles["member"] != 2 {
		t.Fatalf("role counts = %v, want master:1 writer:1 member:2", roles)
	}
	if ssh == nil {
		t.Fatal("SSH member with label alice@laptop not enumerated")
	}
	wantKey, wantLabel := sshPublicForm(sshLine)
	if ssh.Key != wantKey || ssh.Label != wantLabel {
		t.Fatalf("SSH entry = {%q, %q}, want {%q, %q}", ssh.Key, ssh.Label, wantKey, wantLabel)
	}
}

// TestMembersNonMaster: only a current master may enumerate. A writer (a
// MasterIdentity by type, but not in the master set), an outsider master, and a
// flat keyslot all yield ErrNotMaster.
func TestMembersNonMaster(t *testing.T) {
	masterR, masterID, _ := GenerateMaster()
	writerR, writerID, _ := GenerateMaster()
	memberR, _, _ := GenerateX25519()
	dek := freshDEK(t)

	blob, err := SealKeyslot(dek, Membership{
		Masters: []MasterRecipient{masterR},
		Writers: []WriterRecipient{writerR},
		Members: []Recipient{memberR},
	}, masterID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Members(blob, writerID); !errors.Is(err, ErrNotMaster) {
		t.Fatalf("Members as writer = %v, want ErrNotMaster", err)
	}
	_, outsiderID, _ := GenerateMaster()
	if _, err := Members(blob, outsiderID); !errors.Is(err, ErrNotMaster) {
		t.Fatalf("Members as outsider master = %v, want ErrNotMaster", err)
	}
	flat, err := SealKeyslot(dek, Membership{Members: []Recipient{memberR}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Members(flat, masterID); !errors.Is(err, ErrNotMaster) {
		t.Fatalf("Members on a flat keyslot = %v, want ErrNotMaster", err)
	}
}

// TestMembersDriftFree: re-sealing a keyslot to a new membership (the Rewrap path)
// updates the enumerable set with no drift.
func TestMembersDriftFree(t *testing.T) {
	masterR, masterID, _ := GenerateMaster()
	m1R, _, _ := GenerateX25519()
	m2R, _, _ := GenerateX25519()
	dek := freshDEK(t)

	blob, err := SealKeyslot(dek, Membership{Masters: []MasterRecipient{masterR}, Members: []Recipient{m1R}}, masterID)
	if err != nil {
		t.Fatal(err)
	}
	if ms, err := Members(blob, masterID); err != nil || len(ms) != 2 { // master + m1
		t.Fatalf("initial Members = (%d, %v), want 2 entries", len(ms), err)
	}

	resealed, err := ResealKeyslot(blob, masterID, dek, Membership{Masters: []MasterRecipient{masterR}, Members: []Recipient{m1R, m2R}})
	if err != nil {
		t.Fatal(err)
	}
	if ms, err := Members(resealed, masterID); err != nil || len(ms) != 3 { // master + m1 + m2
		t.Fatalf("Members after reseal = (%d, %v), want 3 entries", len(ms), err)
	}
}

// TestMembersOverlongFieldRejected: a member whose recorded field would overflow
// its uint16 length prefix is rejected at seal time, not silently truncated (an
// SSH key comment has no length ceiling).
func TestMembersOverlongFieldRejected(t *testing.T) {
	masterR, masterID, _ := GenerateMaster()
	auth, _ := sshKeypair(t)
	line := append(bytes.TrimSpace(auth), []byte(" "+strings.Repeat("x", 70000))...) // 70 KB comment → label
	memberR, err := SSHRecipient(line)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SealKeyslot(freshDEK(t), Membership{Masters: []MasterRecipient{masterR}, Members: []Recipient{memberR}}, masterID); err == nil {
		t.Fatal("SealKeyslot with a 70 KB member label succeeded; want a refusal")
	}
}

// TestParseAuthorizedKeys: a multi-line authorized_keys file (blank lines and #
// comments skipped) parses into recipients and masters; an empty file and a
// malformed line are errors.
func TestParseAuthorizedKeys(t *testing.T) {
	a, _ := sshKeypair(t)
	b, _ := sshKeypair(t)
	file := []byte("# a comment\n" + string(a) + "   \n" + string(b) + "# trailing comment\n")

	rs, err := ParseAuthorizedKeys(file)
	if err != nil {
		t.Fatalf("ParseAuthorizedKeys: %v", err)
	}
	if len(rs) != 2 {
		t.Fatalf("got %d recipients, want 2", len(rs))
	}
	ms, err := ParseAuthorizedMasterKeys(file)
	if err != nil {
		t.Fatalf("ParseAuthorizedMasterKeys: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("got %d masters, want 2", len(ms))
	}

	if _, err := ParseAuthorizedKeys([]byte("# only comments\n\n   \n")); err == nil {
		t.Fatal("empty authorized_keys input: want an error")
	}
	if _, err := ParseAuthorizedKeys([]byte(string(a) + "not-a-valid-key\n")); err == nil {
		t.Fatal("malformed authorized_keys line: want an error")
	}
}
