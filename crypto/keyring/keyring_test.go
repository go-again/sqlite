package keyring

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"testing"

	"golang.org/x/crypto/ssh"
)

// sshKeypair returns an unencrypted ed25519 SSH key: the authorized_keys line
// (a recipient) and the private key PEM (an identity).
func sshKeypair(t *testing.T) (authLine, privPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return ssh.MarshalAuthorizedKey(sshPub), pem.EncodeToMemory(block)
}

// TestMultiRecipientSSH: a data key wrapped to TWO different SSH recipients is
// recoverable by either one's identity (no shared secret), and a third key is
// rejected — the headline multi-key guarantee.
func TestMultiRecipientSSH(t *testing.T) {
	aliceAuth, alicePEM := sshKeypair(t)
	bobAuth, bobPEM := sshKeypair(t)
	dek := bytes.Repeat([]byte{0xAB}, 32)

	aliceR, err := SSHRecipient(aliceAuth)
	if err != nil {
		t.Fatal(err)
	}
	bobR, err := SSHRecipient(bobAuth)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := Wrap(dek, aliceR, bobR)
	if err != nil {
		t.Fatal(err)
	}

	for name, pem := range map[string][]byte{"alice": alicePEM, "bob": bobPEM} {
		id, err := SSHIdentity(pem, nil)
		if err != nil {
			t.Fatalf("%s identity: %v", name, err)
		}
		if got, err := Unwrap(blob, id); err != nil || !bytes.Equal(got, dek) {
			t.Fatalf("unwrap as %s = (%x, %v)", name, got, err)
		}
	}

	// A third SSH key cannot.
	_, evePEM := sshKeypair(t)
	eveID, err := SSHIdentity(evePEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Unwrap(blob, eveID); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("unwrap with an unlisted key = %v, want ErrNoMatch", err)
	}
}

// TestPassphrase: a passphrase recipient round-trips (single-recipient mode —
// age forbids mixing a passphrase with key recipients), and a wrong passphrase
// is rejected.
func TestPassphrase(t *testing.T) {
	const pass = "correct horse battery staple"
	dek := bytes.Repeat([]byte{0xCD}, 64)

	r, err := PassphraseRecipient([]byte(pass))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := Wrap(dek, r)
	if err != nil {
		t.Fatal(err)
	}
	id, err := PassphraseIdentity([]byte(pass))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Unwrap(blob, id); err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("unwrap with passphrase = (%x, %v)", got, err)
	}
	wrong, _ := PassphraseIdentity([]byte("nope"))
	if _, err := Unwrap(blob, wrong); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("unwrap with wrong passphrase = %v, want ErrNoMatch", err)
	}
}

// TestEncryptedSSHKey: an SSH private key protected by a passphrase round-trips
// when the passphrase is supplied.
func TestEncryptedSSHKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, _ := ssh.NewPublicKey(pub)
	const keyPass = "unlock-me"
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(keyPass))
	if err != nil {
		t.Fatal(err)
	}
	privPEM := pem.EncodeToMemory(block)

	r, err := SSHRecipient(ssh.MarshalAuthorizedKey(sshPub))
	if err != nil {
		t.Fatal(err)
	}
	dek := bytes.Repeat([]byte{0x42}, 32)
	blob, err := Wrap(dek, r)
	if err != nil {
		t.Fatal(err)
	}

	// Wrong/empty passphrase fails to load the identity; the right one works.
	if _, err := SSHIdentity(privPEM, nil); err == nil {
		t.Error("loading an encrypted key with no passphrase: want error")
	}
	id, err := SSHIdentity(privPEM, []byte(keyPass))
	if err != nil {
		t.Fatalf("load encrypted SSH key: %v", err)
	}
	if got, err := Unwrap(blob, id); err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("unwrap with encrypted SSH identity = (%x, %v)", got, err)
	}
}
