// Package keyring wraps a database's data key for multiple recipients — SSH
// keys, passphrases, or age recipients — so several parties can each open the
// same encrypted database with their own identity, without sharing one secret.
// It is the envelope layer behind multi-recipient support in
// gosqlite.org/vfs/vault: a random data key encrypts the database, and that
// data key is [Wrap]ped once per recipient into a small blob any one of them can
// [Unwrap].
//
// It is a thin, sealed layer over filippo.io/age, reusing its audited recipient
// format. The public API never exposes age types; build recipients and
// identities with the loaders here.
package keyring

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"golang.org/x/crypto/ssh"
)

// ErrNoMatch is returned by [Unwrap] when none of the supplied identities can
// recover the data key. Test for it with [errors.Is].
var ErrNoMatch = errors.New("keyring: no supplied identity could unwrap the data key")

// Recipient is a party granted access to a database (encrypt-to). The interface
// is sealed: build one with [SSHRecipient], [PassphraseRecipient], or another
// loader in this package.
type Recipient interface{ ageRecipient() age.Recipient }

// Identity recovers access to a database (decrypt-with). Build one with
// [SSHIdentity], [PassphraseIdentity], or another loader.
type Identity interface{ ageIdentity() age.Identity }

type recipient struct{ r age.Recipient }

func (r recipient) ageRecipient() age.Recipient { return r.r }

type identity struct{ i age.Identity }

func (i identity) ageIdentity() age.Identity { return i.i }

// SSHRecipient builds a recipient from one authorized_keys line (ssh-ed25519 or
// ssh-rsa) — e.g. the contents of a colleague's id_ed25519.pub.
func SSHRecipient(authorizedKeyLine []byte) (Recipient, error) {
	r, err := agessh.ParseRecipient(string(bytes.TrimSpace(authorizedKeyLine)))
	if err != nil {
		return nil, fmt.Errorf("keyring: SSH recipient: %w", err)
	}
	return recipient{r}, nil
}

// SSHIdentity builds an identity from an SSH private key in PEM form (ed25519 or
// RSA). Pass the key's passphrase if it is encrypted; nil/empty for an
// unencrypted key.
func SSHIdentity(pemPrivateKey, passphrase []byte) (Identity, error) {
	if len(passphrase) == 0 {
		i, err := agessh.ParseIdentity(pemPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("keyring: SSH identity: %w", err)
		}
		return identity{i}, nil
	}
	k, err := ssh.ParseRawPrivateKeyWithPassphrase(pemPrivateKey, passphrase)
	if err != nil {
		return nil, fmt.Errorf("keyring: SSH identity: %w", err)
	}
	var ai age.Identity
	switch key := k.(type) {
	case *ed25519.PrivateKey:
		ai, err = agessh.NewEd25519Identity(*key)
	case ed25519.PrivateKey:
		ai, err = agessh.NewEd25519Identity(key)
	case *rsa.PrivateKey:
		ai, err = agessh.NewRSAIdentity(key)
	default:
		return nil, fmt.Errorf("keyring: unsupported SSH key type %T", k)
	}
	if err != nil {
		return nil, fmt.Errorf("keyring: SSH identity: %w", err)
	}
	return identity{ai}, nil
}

// GenerateX25519 returns a fresh native age keypair: the Recipient to grant
// access and the Identity to recover it — key-based access without SSH or a
// shared passphrase (and the building block for tests).
func GenerateX25519() (Recipient, Identity, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, nil, fmt.Errorf("keyring: generate X25519: %w", err)
	}
	return recipient{id.Recipient()}, identity{id}, nil
}

// PassphraseRecipient builds a recipient from a shared passphrase (scrypt) — a
// secret rather than a keypair. A passphrase recipient cannot be combined with
// any other recipient in a single [Wrap] (an age restriction): it is the whole
// recipient set, or use key recipients ([SSHRecipient], …) for multiple parties.
func PassphraseRecipient(passphrase []byte) (Recipient, error) {
	r, err := age.NewScryptRecipient(string(passphrase))
	if err != nil {
		return nil, fmt.Errorf("keyring: passphrase recipient: %w", err)
	}
	return recipient{r}, nil
}

// PassphraseIdentity builds the identity matching [PassphraseRecipient].
func PassphraseIdentity(passphrase []byte) (Identity, error) {
	i, err := age.NewScryptIdentity(string(passphrase))
	if err != nil {
		return nil, fmt.Errorf("keyring: passphrase identity: %w", err)
	}
	return identity{i}, nil
}

// Wrap encrypts dataKey to each recipient and returns a compact binary blob that
// any one of them can [Unwrap]. At least one recipient is required.
func Wrap(dataKey []byte, to ...Recipient) ([]byte, error) {
	if len(to) == 0 {
		return nil, errors.New("keyring: Wrap needs at least one recipient")
	}
	rs := make([]age.Recipient, len(to))
	for i, r := range to {
		rs[i] = r.ageRecipient()
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rs...)
	if err != nil {
		return nil, fmt.Errorf("keyring: wrap: %w", err)
	}
	if _, err := w.Write(dataKey); err != nil {
		return nil, fmt.Errorf("keyring: wrap: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("keyring: wrap: %w", err)
	}
	return buf.Bytes(), nil
}

// Unwrap recovers the data key from a blob produced by [Wrap] using the first
// identity that matches, or [ErrNoMatch] if none do.
func Unwrap(blob []byte, with ...Identity) ([]byte, error) {
	ids := make([]age.Identity, len(with))
	for i, id := range with {
		ids[i] = id.ageIdentity()
	}
	r, err := age.Decrypt(bytes.NewReader(blob), ids...)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return nil, ErrNoMatch
		}
		return nil, fmt.Errorf("keyring: unwrap: %w", err)
	}
	key, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("keyring: unwrap: %w", err)
	}
	return key, nil
}
