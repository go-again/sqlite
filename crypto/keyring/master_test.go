package keyring

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func freshDEK(t *testing.T) []byte {
	t.Helper()
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		t.Fatal(err)
	}
	return dek
}

// TestMasterSealOpen: a master seals a keyslot for itself and a member; both open
// it when pinning the master, and the data key round-trips.
func TestMasterSealOpen(t *testing.T) {
	masterR, masterID, err := GenerateMaster()
	if err != nil {
		t.Fatal(err)
	}
	memberR, memberID, err := GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	dek := freshDEK(t)

	blob, err := SealKeyslot(dek, Membership{Masters: []MasterRecipient{masterR}, Members: []Recipient{memberR}}, masterID)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	trusted := []MasterRecipient{masterR}
	for name, id := range map[string]Identity{"master": masterID, "member": memberID} {
		got, _, err := OpenKeyslot(blob, trusted, id)
		if err != nil {
			t.Fatalf("open as %s: %v", name, err)
		}
		if !bytes.Equal(got, dek) {
			t.Fatalf("open as %s: data key mismatch", name)
		}
	}
}

// TestMasterMultiple: two masters, either may sign; a reader pinning both accepts.
func TestMasterMultiple(t *testing.T) {
	m1R, _, _ := GenerateMaster()
	m2R, m2ID, _ := GenerateMaster()
	memberR, memberID, _ := GenerateX25519()
	dek := freshDEK(t)

	blob, err := SealKeyslot(dek, Membership{Masters: []MasterRecipient{m1R, m2R}, Members: []Recipient{memberR}}, m2ID)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, _, err := OpenKeyslot(blob, []MasterRecipient{m1R, m2R}, memberID); err != nil {
		t.Fatalf("open pinning both masters: %v", err)
	}
}

// TestWriterSignVerify: the authorized writer set returned by OpenKeyslot verifies
// a committed-state signature; a non-writer's signature does not.
func TestWriterSignVerify(t *testing.T) {
	masterR, masterID, _ := GenerateMaster()
	writerR, writerID, _ := GenerateMaster()
	memberR, memberID, _ := GenerateX25519()
	dek := freshDEK(t)

	blob, err := SealKeyslot(dek, Membership{
		Masters: []MasterRecipient{masterR},
		Writers: []WriterRecipient{writerR},
		Members: []Recipient{memberR},
	}, masterID)
	if err != nil {
		t.Fatal(err)
	}

	_, writers, err := OpenKeyslot(blob, []MasterRecipient{masterR}, memberID)
	if err != nil {
		t.Fatal(err)
	}
	if len(writers) != 1 {
		t.Fatalf("got %d authorized writers, want 1", len(writers))
	}

	msg := []byte("committed-state-bytes")
	if !VerifyState(writers, msg, SignState(writerID, msg)) {
		t.Fatal("writer signature did not verify against the authorized writers")
	}
	// The master is not in the writer set, so its signature must not verify.
	if VerifyState(writers, msg, SignState(masterID, msg)) {
		t.Fatal("non-writer (master) signature verified as a writer")
	}
}

// TestForgeryRejected: a member with the data key re-seals under its OWN master
// key. A reader pinning the REAL master rejects it — the heart of the model.
func TestForgeryRejected(t *testing.T) {
	realR, realID, _ := GenerateMaster()
	memberR, memberID, _ := GenerateX25519()
	dek := freshDEK(t)

	blob, _ := SealKeyslot(dek, Membership{Masters: []MasterRecipient{realR}, Members: []Recipient{memberR}}, realID)

	got, _, err := OpenKeyslot(blob, []MasterRecipient{realR}, memberID)
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("member read: %v", err)
	}
	rogueR, rogueID, _ := GenerateMaster()
	outsiderR, outsiderID, _ := GenerateX25519()
	forged, err := SealKeyslot(dek, Membership{Masters: []MasterRecipient{rogueR}, Members: []Recipient{outsiderR}}, rogueID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenKeyslot(forged, []MasterRecipient{realR}, outsiderID); !errors.Is(err, ErrUnauthorizedKeyslot) {
		t.Fatalf("forged keyslot opened as %v, want ErrUnauthorizedKeyslot", err)
	}
}

// TestTamperRejected: flipping a byte in a sealed keyslot fails the signature.
func TestTamperRejected(t *testing.T) {
	masterR, masterID, _ := GenerateMaster()
	memberR, _, _ := GenerateX25519()
	dek := freshDEK(t)
	blob, _ := SealKeyslot(dek, Membership{Masters: []MasterRecipient{masterR}, Members: []Recipient{memberR}}, masterID)

	blob[len(blob)/2] ^= 0xff
	if _, _, err := OpenKeyslot(blob, []MasterRecipient{masterR}, masterID); err == nil {
		t.Fatal("tampered keyslot opened; want an error")
	}
}

// TestDowngradeRejected: a reader that pins masters refuses a flat (unsigned)
// keyslot — a member cannot strip the master protection.
func TestDowngradeRejected(t *testing.T) {
	masterR, _, _ := GenerateMaster()
	memberR, memberID, _ := GenerateX25519()
	dek := freshDEK(t)

	flat, err := SealKeyslot(dek, Membership{Members: []Recipient{memberR}}, nil) // no masters: flat
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenKeyslot(flat, nil, memberID); err != nil {
		t.Fatalf("flat open: %v", err)
	}
	if _, _, err := OpenKeyslot(flat, []MasterRecipient{masterR}, memberID); !errors.Is(err, ErrUnauthorizedKeyslot) {
		t.Fatalf("flat keyslot accepted under pinning = %v, want ErrUnauthorizedKeyslot", err)
	}
}

// TestSealRequiresPinnedSigner: the signing identity must be one of the masters.
func TestSealRequiresPinnedSigner(t *testing.T) {
	m1R, _, _ := GenerateMaster()
	_, m2ID, _ := GenerateMaster() // not pinned
	memberR, _, _ := GenerateX25519()
	dek := freshDEK(t)

	if _, err := SealKeyslot(dek, Membership{Masters: []MasterRecipient{m1R}, Members: []Recipient{memberR}}, m2ID); err == nil {
		t.Fatal("seal with an unpinned signer: want error")
	}
	if _, err := SealKeyslot(dek, Membership{Masters: []MasterRecipient{m1R}, Members: []Recipient{memberR}}, nil); err == nil {
		t.Fatal("seal with masters but no signer: want error")
	}
}
