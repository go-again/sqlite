// Command vault-blobstore shows a blobstore over a multi-recipient, tamper-evident
// database. A blobstore is just SQL and incremental BLOB I/O over a *sqlite.DB, so
// whatever VFS protects that database protects the store — no blobstore-specific
// configuration. Here the VFS is gosqlite.org/vfs/vault: the container is encrypted
// to several recipients (each opens with their own key, no shared secret), the data
// is compressed and then encrypted, and authenticated mode makes tampering or
// rollback detectable.
//
// The flow: write an object into a store encrypted to Alice and Bob; confirm the
// plaintext is absent from the raw file; reopen as Alice and as Bob and read it
// back; confirm a stranger (not a recipient) cannot open it at all.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	sqlite "gosqlite.org"
	"gosqlite.org/blobstore"
	"gosqlite.org/crypto/keyring"
	"gosqlite.org/vfs/vault"
)

func main() {
	dir, err := os.MkdirTemp("", "vault-blobstore-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	n, err := roundTrip(dir)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("multi-recipient vault blobstore OK: %d bytes round-tripped by both recipients; plaintext absent, stranger refused\n", n)
}

// roundTrip writes an object into a blobstore whose database is encrypted to two
// recipients (compressed, then encrypted, then authenticated), verifies the
// plaintext is not on disk, reads it back as each recipient, and confirms a
// non-recipient is refused. It returns the number of bytes verified.
func roundTrip(dir string) (int, error) {
	path := filepath.Join(dir, "shared.db")
	payload := []byte("the quick brown fox jumps over the lazy dog — secret, shared by two parties")

	// Each recipient is an age-style key — here a generated X25519 pair; in
	// practice an SSH key (keyring.SSHRecipient) or a passphrase
	// (keyring.PassphraseRecipient) works the same way.
	alice, aliceID, err := keyring.GenerateX25519()
	if err != nil {
		return 0, err
	}
	bob, bobID, err := keyring.GenerateX25519()
	if err != nil {
		return 0, err
	}
	_, strangerID, err := keyring.GenerateX25519() // not a recipient
	if err != nil {
		return 0, err
	}

	// Create: a random data key encrypts the container and is wrapped once per
	// recipient into a keyslot inside the file. Compression and authenticated mode
	// are independent options that compose with encryption.
	create := vault.Options{
		Recipients:   []keyring.Recipient{alice, bob},
		Level:        vault.CompressionDefault,
		Authenticate: true,
	}
	var id int64
	if err := withStore(path, create, func(store *blobstore.Store) error {
		oid, err := store.Create(context.Background())
		if err != nil {
			return err
		}
		id = oid
		w, err := store.Writer(context.Background(), oid)
		if err != nil {
			return err
		}
		if _, err := w.WriteAt(payload, 0); err != nil {
			return err
		}
		return w.Close()
	}); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}

	// The container encrypted every page, so the plaintext must not appear on disk.
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if bytes.Contains(raw, payload) {
		return 0, fmt.Errorf("plaintext found in %s — not encrypted at rest", path)
	}

	// Either recipient opens the store with their own identity — no shared secret.
	for who, id2 := range map[string]keyring.Identity{"alice": aliceID, "bob": bobID} {
		got, err := readBack(path, id2, id)
		if err != nil {
			return 0, fmt.Errorf("read as %s: %w", who, err)
		}
		if !bytes.Equal(got, payload) {
			return 0, fmt.Errorf("read as %s: round-trip mismatch: got %q", who, got)
		}
	}

	// A non-recipient cannot open the database at all: no keyslot unwraps for them,
	// so the open is refused (internally vault.ErrNoIdentity, flattened through
	// SQLite's open path).
	if _, err := readBack(path, strangerID, id); err == nil {
		return 0, fmt.Errorf("stranger opened a database they are not a recipient of")
	}

	return len(payload), nil
}

// readBack opens the store as a single recipient identity and reads object id.
func readBack(path string, who keyring.Identity, id int64) ([]byte, error) {
	var got []byte
	err := withStore(path, vault.Options{Identities: []keyring.Identity{who}}, func(store *blobstore.Store) error {
		size, err := store.Size(context.Background(), id)
		if err != nil {
			return err
		}
		r, err := store.Reader(context.Background(), id)
		if err != nil {
			return err
		}
		defer r.Close()
		got, err = io.ReadAll(io.NewSectionReader(r, 0, size))
		return err
	})
	return got, err
}

// withStore opens the vault database with opts, runs a blobstore over it through
// fn, and closes both. The store writes raw chunks; the container compresses then
// encrypts each block (Options.Level + the recipients' data key), so compression
// lives in one place rather than per-chunk in the store.
func withStore(path string, opts vault.Options, fn func(*blobstore.Store) error) error {
	db, err := vault.Open(sqlite.Config{Path: path, Pragmas: sqlite.RecommendedPragmas()}, opts)
	if err != nil {
		return err
	}
	defer db.Close()
	store, err := blobstore.Open(db, "files")
	if err != nil {
		return err
	}
	return fn(store)
}
