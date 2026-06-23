// Command encrypted-blobstore shows that a blobstore inherits encryption at rest
// for free: open the store's database through gosqlite.org/vfs/crypto and every
// object, chunk, and block it writes is encrypted on disk, with no
// blobstore-specific configuration. The whole "does encryption compose with
// blobstore?" question reduces to composition — the store is just SQL and
// incremental BLOB I/O over a *sqlite.DB, so whatever VFS encrypts that database
// encrypts the store.
//
// vfs/crypto here is confidentiality-only with a single raw key. For
// multi-recipient or tamper-evident encryption under a blobstore, the same
// composition works with gosqlite.org/vfs/vault.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	sqlite "gosqlite.org"
	"gosqlite.org/blobstore"
	"gosqlite.org/vfs/crypto"
)

func main() {
	dir, err := os.MkdirTemp("", "encrypted-blobstore-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	n, err := roundTrip(dir)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("encrypted blobstore OK: %d bytes round-tripped; plaintext absent on disk\n", n)
}

// roundTrip writes an object into a blobstore whose database is encrypted at
// rest, confirms the payload is not present in the raw file, then reopens with
// the same key and reads it back. It returns the number of bytes verified.
func roundTrip(dir string) (int, error) {
	path := filepath.Join(dir, "vault.db")
	payload := []byte("the quick brown fox jumps over the lazy dog — secret at rest")

	key := make([]byte, crypto.KeyLen(crypto.Adiantum)) // 32-byte Adiantum key
	if _, err := rand.Read(key); err != nil {
		return 0, err
	}

	// Write: encrypt the database, then run a blobstore over it.
	var id int64
	if err := withStore(path, key, func(store *blobstore.Store) error {
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

	// The VFS encrypted every page, so the plaintext must not appear on disk.
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if bytes.Contains(raw, payload) {
		return 0, fmt.Errorf("plaintext found in %s — not encrypted at rest", path)
	}

	// Reopen with the same key and read it back.
	var got []byte
	if err := withStore(path, key, func(store *blobstore.Store) error {
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
	}); err != nil {
		return 0, fmt.Errorf("read: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return 0, fmt.Errorf("round-trip mismatch: got %q", got)
	}
	return len(got), nil
}

// withStore opens an encrypted database with key, runs a compressed blobstore
// over it through fn, and closes both. Compression is incidental — it shows the
// two composing cleanly: the store compresses each chunk, the VFS then encrypts
// each page (compress-then-encrypt, the correct order).
func withStore(path string, key []byte, fn func(*blobstore.Store) error) error {
	db, err := crypto.Open(sqlite.Config{Path: path, Pragmas: sqlite.RecommendedPragmas()}, crypto.Options{Key: key})
	if err != nil {
		return err
	}
	defer db.Close()
	store, err := blobstore.Open(db, "files", blobstore.WithCompression(blobstore.CompressionDefault))
	if err != nil {
		return err
	}
	return fn(store)
}
