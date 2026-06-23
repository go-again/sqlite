// vault example: a tour of the container's capabilities.
//
// vfs/vault stores a SQLite database in a block-structured container where
// compression and encryption are independent options, so a database can be plain,
// compressed, encrypted (single-key or to several recipients), authenticated
// (tamper-evident — symmetric or writer-signed for read-only recipients),
// rollback-resistant (an external anchor), or any combination — queried live in
// place, or shipped as a snapshot. Each step below demonstrates one cell of that
// matrix and prints what it proved.
//
// Run from the vault module:
//
//	cd vfs/vault && go run ./example
package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
	"gosqlite.org/vfs/vault"
)

const rows = 2000

// row is ~2.8 KB of very compressible text — a clear plaintext marker, too.
var row = strings.Repeat("the quick brown fox jumps over the lazy dog ", 64)

func main() {
	dir, err := os.MkdirTemp("", "vfs-vault-example-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	logical := int64(len(row)) * rows

	plain(filepath.Join(dir, "plain.db"))
	compressed(filepath.Join(dir, "compressed.db"), logical)
	encrypted(filepath.Join(dir, "encrypted.db"))
	both(filepath.Join(dir, "both.db"), logical)
	multiRecipient(filepath.Join(dir, "shared.db"), logical)
	authenticated(filepath.Join(dir, "authenticated.db"))
	antiReplay(filepath.Join(dir, "anti-replay.db"))
	writerSigned(filepath.Join(dir, "writer-signed.db"))
	snapshot(filepath.Join(dir, "snapshot.db"), logical)
}

// plain: the default container — no compression, no encryption. The at-rest file
// is the container (VAULTv01 magic), never raw SQLite, and round-trips in place.
func plain(path string) {
	withDB(path, vault.Options{}, seed)
	fmt.Printf("plain:            at-rest magic %q (a container, not raw SQLite); %d rows round-trip\n",
		head(path, 8), readCount(path, vault.Options{}))
}

// compressed: Options.Level turns on compression — pages are compressed then
// stored, so the at-rest file is a fraction of the logical size.
func compressed(path string, logical int64) {
	withDB(path, vault.Options{Level: vault.CompressionDefault}, seed)
	fmt.Printf("compressed:       %s\n", ratio(path, logical))
}

// encrypted: Options.Key encrypts every block at rest with a single raw key. The
// plaintext never hits disk; reopening needs the key.
func encrypted(path string) {
	key := randomKey()
	withDB(path, vault.Options{Key: key}, seed)
	if containsPlaintext(path) {
		log.Fatal("encrypted: plaintext found at rest")
	}
	fmt.Printf("encrypted:        plaintext absent at rest; %d rows round-trip with the key\n",
		readCount(path, vault.Options{Key: key}))
}

// both: compression and encryption compose (compress then encrypt) — a smaller
// file with no plaintext at rest.
func both(path string, logical int64) {
	key := randomKey()
	withDB(path, vault.Options{Level: vault.CompressionDefault, Key: key}, seed)
	if containsPlaintext(path) {
		log.Fatal("both: plaintext found at rest")
	}
	fmt.Printf("compress+encrypt: %s; plaintext absent at rest\n", ratio(path, logical))
}

// multiRecipient: Options.Recipients wraps a random data key for each recipient
// (the age model), so several parties open one database with their own key and no
// shared secret. Compression composes orthogonally. A non-recipient is refused.
func multiRecipient(path string, logical int64) {
	alice, aliceID := genRecipient()
	bob, bobID := genRecipient()
	_, strangerID := genRecipient()

	withDB(path, vault.Options{
		Recipients: []keyring.Recipient{alice, bob},
		Level:      vault.CompressionDefault,
	}, seed)

	na := readCount(path, vault.Options{Identities: []keyring.Identity{aliceID}})
	nb := readCount(path, vault.Options{Identities: []keyring.Identity{bobID}})
	if na != rows || nb != rows {
		log.Fatalf("multi-recipient: recipient read counts (alice=%d, bob=%d), want %d each", na, nb, rows)
	}
	if db, err := vault.Open(sqlite.Config{Path: path}, vault.Options{Identities: []keyring.Identity{strangerID}}); err == nil {
		_ = db.Close()
		log.Fatal("multi-recipient: a non-recipient opened the database")
	}
	fmt.Printf("multi-recipient:  %s, encrypted to 2 recipients; alice and bob each read %d rows, stranger refused\n",
		ratio(path, logical), na)
}

// authenticated: Options.Authenticate adds a symmetric MAC'd root (keyed by the
// data key) plus a per-slot hash, so a tampered or partially-rolled-back container
// is rejected — the open or the first read fails. Membership in the signed state
// also means the flag cannot be stripped. It is tamper-evident, NOT anti-replay: a
// full rollback to a complete earlier image needs Options.Anchor (see antiReplay).
func authenticated(path string) {
	key := randomKey()
	withDB(path, vault.Options{Key: key, Authenticate: true}, seed)

	// Flip a run of bytes in the encrypted body to simulate tampering.
	raw, err := os.ReadFile(path)
	fatal(err)
	for i := range 64 {
		raw[len(raw)/2+i] ^= 0xff
	}
	fatal(os.WriteFile(path, raw, 0o600))

	detected := false
	db, err := vault.Open(sqlite.Config{Path: path}, vault.Options{Key: key, Authenticate: true})
	if err != nil {
		detected = true
	} else {
		var n int
		detected = db.QueryRow(`SELECT count(*) FROM notes`).Scan(&n) != nil
		_ = db.Close()
	}
	if !detected {
		log.Fatal("authenticated: tampering went undetected")
	}
	fmt.Println("authenticated:    a flipped byte is detected on reopen (tamper-evident; add Options.Anchor for rollback resistance)")
}

// antiReplay: Options.Anchor — an external monotonic counter kept OFF the file —
// upgrades authenticated mode to rollback-resistant. Authenticated mode alone
// detects tampering and a partial rollback, but a COMPLETE earlier image is still
// validly signed; the anchor records each commit's generation and rejects a file
// rolled back below the recorded floor.
func antiReplay(path string) {
	key := randomKey()
	anchor := vault.FileAnchor(path + ".floor") // a TPM/keystore counter in production
	opts := vault.Options{Key: key, Authenticate: true, Anchor: anchor}

	// Write a first state, then snapshot the (validly signed) file at that generation.
	withDB(path, opts, seed)
	snap, err := os.ReadFile(path)
	fatal(err)

	// Advance the database (and the anchor) with a second committed state.
	db, err := vault.Open(sqlite.Config{Path: path}, opts)
	fatal(err)
	if _, err := db.Exec(`INSERT INTO notes (body) VALUES (?)`, row); err != nil {
		log.Fatal(err)
	}
	fatal(db.Close())

	// Roll the file back to the snapshot: it is complete and validly signed, but the
	// anchor floor has moved past it, so reopening is rejected.
	fatal(os.WriteFile(path, snap, 0o600))
	detected := false
	if rdb, err := vault.Open(sqlite.Config{Path: path}, opts); err != nil {
		detected = true
	} else {
		var n int
		detected = rdb.QueryRow(`SELECT count(*) FROM notes`).Scan(&n) != nil
		_ = rdb.Close()
	}
	if !detected {
		log.Fatal("anti-replay: a rolled-back database was accepted")
	}
	fmt.Println("anti-replay:      a rolled-back (but validly signed) image is rejected via Options.Anchor")
}

// writerSigned: the asymmetric authenticated flavour. An admin (master) signs the
// keyslot and authorizes a writer; commits are signed by a writer (ed25519), so a
// recipient holding the read key who is NOT a writer can read and verify but cannot
// forge a write others accept — it is read-only. A reader's trust anchor is the
// admin it pins; a keyslot not signed by that admin is rejected.
func writerSigned(path string) {
	admin, adminID := genMaster()   // administers the keyslot + bootstraps the data
	writer, writerID := genMaster() // an authorized writer, not an admin
	reader, readerID := genRecipient()
	wrongAdmin, _ := genMaster()

	// Create: the admin signs the membership (SignWith) and writes the initial data
	// as a writer (WriteAs); the separate writer and the reader are authorized too.
	db, err := vault.Open(sqlite.Config{Path: path}, vault.Options{
		Masters:    []keyring.MasterRecipient{admin},
		SignWith:   adminID,
		Writers:    []keyring.WriterRecipient{admin, writer},
		WriteAs:    adminID,
		Recipients: []keyring.Recipient{reader},
	})
	fatal(err)
	seed(db)
	fatal(db.Close())

	trust := []keyring.MasterRecipient{admin}

	// The authorized writer (not an admin) reopens and appends a signed commit.
	wdb, err := vault.Open(sqlite.Config{Path: path}, vault.Options{
		Identities: []keyring.Identity{writerID}, Masters: trust, WriteAs: writerID,
	})
	fatal(err)
	_, err = wdb.Exec(`INSERT INTO notes (body) VALUES (?)`, row)
	fatal(err)
	fatal(wdb.Close())

	// The read-only member reads (verifying the writer signature) but cannot write.
	rdb, err := vault.Open(sqlite.Config{Path: path}, vault.Options{
		Identities: []keyring.Identity{readerID}, Masters: trust,
	})
	fatal(err)
	var n int
	fatal(rdb.QueryRow(`SELECT count(*) FROM notes`).Scan(&n))
	if _, err := rdb.Exec(`INSERT INTO notes (body) VALUES ('nope')`); err == nil {
		log.Fatal("writer-signed: a read-only member was allowed to write")
	}
	fatal(rdb.Close())

	// A reader pinning the wrong admin is rejected: the keyslot is not signed by it.
	if bad, err := vault.Open(sqlite.Config{Path: path}, vault.Options{
		Identities: []keyring.Identity{readerID}, Masters: []keyring.MasterRecipient{wrongAdmin},
	}); err == nil {
		_ = bad.Close()
		log.Fatal("writer-signed: a reader pinning the wrong admin was admitted")
	}

	fmt.Printf("writer-signed:    admin authorized a writer; the writer appended (now %d rows), the read-only member was refused writes, wrong-admin rejected\n", n)
}

// snapshot: the alternative open model. OpenSnapshot inflates the container into a
// transient working copy while open and repacks at Close — durable per session.
// Good for archival and open-modify-close tooling (no encryption: its working copy
// is plaintext on disk).
func snapshot(path string, logical int64) {
	db, err := vault.OpenSnapshot(sqlite.Config{Path: path}, vault.Options{Level: vault.CompressionDefault})
	fatal(err)
	seed(db)
	fatal(db.Close()) // repacks the working copy to disk
	fmt.Printf("snapshot:         %s after Close (ran from an inflated working copy while open)\n", ratio(path, logical))
}

// withDB opens a live container with opts, runs fn against it, and closes it.
func withDB(path string, opts vault.Options, fn func(*sqlite.DB)) {
	db, err := vault.Open(sqlite.Config{Path: path}, opts)
	fatal(err)
	fn(db)
	fatal(db.Close())
}

// seed creates the table and inserts the rows in one transaction.
func seed(db *sqlite.DB) {
	_, err := db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)`)
	fatal(err)
	tx, err := db.Begin()
	fatal(err)
	stmt, err := tx.Prepare(`INSERT INTO notes (body) VALUES (?)`)
	fatal(err)
	for range rows {
		_, err := stmt.Exec(row)
		fatal(err)
	}
	fatal(stmt.Close())
	fatal(tx.Commit())
}

// readCount reopens the database with opts and returns the row count.
func readCount(path string, opts vault.Options) int {
	db, err := vault.Open(sqlite.Config{Path: path}, opts)
	fatal(err)
	defer db.Close()
	var n int
	fatal(db.QueryRow(`SELECT count(*) FROM notes`).Scan(&n))
	return n
}

// genRecipient returns a fresh X25519 recipient/identity pair. In practice an SSH
// key (keyring.SSHRecipient) or a passphrase (keyring.PassphraseRecipient) works
// the same way.
func genRecipient() (keyring.Recipient, keyring.Identity) {
	r, id, err := keyring.GenerateX25519()
	fatal(err)
	return r, id
}

// genMaster returns a fresh ed25519 keypair usable as an admin (master) or a
// writer — the recipient to pin and the identity to sign with.
func genMaster() (keyring.MasterRecipient, keyring.MasterIdentity) {
	r, id, err := keyring.GenerateMaster()
	fatal(err)
	return r, id
}

// containsPlaintext reports whether the seeded plaintext appears in the raw file.
func containsPlaintext(path string) bool {
	raw, err := os.ReadFile(path)
	fatal(err)
	return bytes.Contains(raw, []byte(row))
}

// ratio formats the at-rest size against the logical content size.
func ratio(path string, logical int64) string {
	info, err := os.Stat(path)
	fatal(err)
	return fmt.Sprintf("on-disk %d bytes (logical ~%d, %.0fx smaller)", info.Size(), logical, float64(logical)/float64(info.Size()))
}

// head returns the first n bytes of the file.
func head(path string, n int) []byte {
	raw, err := os.ReadFile(path)
	fatal(err)
	if len(raw) < n {
		return raw
	}
	return raw[:n]
}

// randomKey returns a fresh 32-byte Adiantum key.
func randomKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		log.Fatal(err)
	}
	return key
}

func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
