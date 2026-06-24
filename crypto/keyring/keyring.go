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
	"strings"

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
type Recipient interface {
	ageRecipient() age.Recipient
	// publicForm returns the recipient's public string — an authorized_keys line
	// (SSH) or an age1… recipient — and an optional human label (an SSH key
	// comment). It is what [Members] enumerates. A passphrase recipient has no
	// enumerable public form and returns "", "".
	publicForm() (key, label string)
}

// Identity recovers access to a database (decrypt-with). Build one with
// [SSHIdentity], [PassphraseIdentity], or another loader.
type Identity interface{ ageIdentity() age.Identity }

type recipient struct {
	r          age.Recipient
	pub, label string // public form + optional label, captured at construction (see publicForm)
}

func (r recipient) ageRecipient() age.Recipient  { return r.r }
func (r recipient) publicForm() (string, string) { return r.pub, r.label }

type identity struct{ i age.Identity }

func (i identity) ageIdentity() age.Identity { return i.i }

// SSHRecipient builds a recipient from one authorized_keys line (ssh-ed25519 or
// ssh-rsa) — e.g. the contents of a colleague's id_ed25519.pub.
func SSHRecipient(authorizedKeyLine []byte) (Recipient, error) {
	line := bytes.TrimSpace(authorizedKeyLine)
	r, err := agessh.ParseRecipient(string(line))
	if err != nil {
		return nil, fmt.Errorf("keyring: SSH recipient: %w", err)
	}
	pub, label := sshPublicForm(line)
	return recipient{r: r, pub: pub, label: label}, nil
}

// sshPublicForm canonicalises an authorized_keys line to its comment-free key
// string (type + base64, the stable identifier) and its trailing comment as a
// label. On a parse failure it falls back to the trimmed line with no label.
func sshPublicForm(line []byte) (key, label string) {
	pub, comment, _, _, err := ssh.ParseAuthorizedKey(line)
	if err != nil {
		return string(bytes.TrimSpace(line)), ""
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), comment
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
	rec := id.Recipient()
	return recipient{r: rec, pub: rec.String()}, identity{id}, nil
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
	return recipient{r: r}, nil // no enumerable public form (see publicForm)
}

// PassphraseIdentity builds the identity matching [PassphraseRecipient].
func PassphraseIdentity(passphrase []byte) (Identity, error) {
	i, err := age.NewScryptIdentity(string(passphrase))
	if err != nil {
		return nil, fmt.Errorf("keyring: passphrase identity: %w", err)
	}
	return identity{i}, nil
}

// ParseAuthorizedKeys parses an authorized_keys-style file — one key per line,
// with blank lines and # comments skipped — into recipients, saving every caller
// the line loop. An unparseable key line fails the whole file (with its line
// number); an empty file is an error. Use [ParseAuthorizedMasterKeys] for masters
// or writers, which must be ed25519.
func ParseAuthorizedKeys(b []byte) ([]Recipient, error) {
	var out []Recipient
	err := eachAuthorizedKeyLine(b, func(line []byte) error {
		r, err := SSHRecipient(line)
		if err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("keyring: no keys in authorized_keys input")
	}
	return out, nil
}

// ParseAuthorizedMasterKeys is [ParseAuthorizedKeys] for masters or writers: it
// parses each line with [SSHMasterRecipient], so every key must be ssh-ed25519.
func ParseAuthorizedMasterKeys(b []byte) ([]MasterRecipient, error) {
	var out []MasterRecipient
	err := eachAuthorizedKeyLine(b, func(line []byte) error {
		r, err := SSHMasterRecipient(line)
		if err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("keyring: no keys in authorized_keys input")
	}
	return out, nil
}

// eachAuthorizedKeyLine invokes fn for every non-blank, non-comment line of an
// authorized_keys file, reporting the 1-based line number on a parse error.
func eachAuthorizedKeyLine(b []byte, fn func(line []byte) error) error {
	for i, raw := range bytes.Split(b, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		if err := fn(line); err != nil {
			return fmt.Errorf("keyring: authorized_keys line %d: %w", i+1, err)
		}
	}
	return nil
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
