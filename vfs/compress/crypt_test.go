package compress

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
	"gosqlite.org/vfs"
	"gosqlite.org/vfs/crypto"
)

// TestLiveEncryptionRoundTrip drives the full stack: an encrypted compressed
// database round-trips its rows, the plaintext never appears at rest, and
// reopening without the key (or with the wrong key) fails.
func TestLiveEncryptionRoundTrip(t *testing.T) {
	const marker = "SUPER_SECRET_MARKER_VALUE_0123456789"
	for _, tc := range []struct {
		name   string
		cipher crypto.Cipher
		keyLen int
	}{
		{"Adiantum", crypto.Adiantum, 32},
		{"AESXTS", crypto.AESXTS, 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "enc.dbz")
			key := make([]byte, tc.keyLen)
			for i := range key {
				key[i] = byte(i*7 + 1)
			}
			opts := Options{Key: key, Cipher: tc.cipher}

			db, err := Open(sqlite.Config{Path: path}, opts)
			if err != nil {
				t.Fatalf("open encrypted: %v", err)
			}
			if _, err := db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
				t.Fatal(err)
			}
			for i := range 200 {
				if _, err := db.Exec(`INSERT INTO t(v) VALUES(?)`, marker+strconv.Itoa(i)); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			// Reopen with the key: rows intact.
			db2, err := Open(sqlite.Config{Path: path}, opts)
			if err != nil {
				t.Fatalf("reopen with key: %v", err)
			}
			var n int
			if err := db2.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			var v string
			if err := db2.QueryRow(`SELECT v FROM t WHERE id = 1`).Scan(&v); err != nil {
				t.Fatal(err)
			}
			_ = db2.Close()
			if n != 200 || v != marker+"0" {
				t.Fatalf("round-trip mismatch: count=%d v=%q", n, v)
			}

			// The plaintext marker must not survive at rest.
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(marker)) {
				t.Error("plaintext marker found in the encrypted file at rest")
			}

			// No key, or the wrong key, must fail to open.
			if db3, err := Open(sqlite.Config{Path: path}, Options{}); err == nil {
				_ = db3.Close()
				t.Error("reopen without a key: want error")
			}
			wrong := make([]byte, tc.keyLen)
			if db4, err := Open(sqlite.Config{Path: path}, Options{Key: wrong, Cipher: tc.cipher}); err == nil {
				_ = db4.Close()
				t.Error("reopen with the wrong key: want error")
			}
		})
	}
}

// TestPassFileEncryptRoundTrip exercises the auxiliary-file (journal/WAL)
// page-aligned read-modify-write encryption directly: assorted aligned, sub-page,
// and spanning writes round-trip, and the plaintext never appears on disk.
func TestPassFileEncryptRoundTrip(t *testing.T) {
	const ps = 4096
	const marker = "PASSFILE_PLAINTEXT_MARKER_XYZ"
	cipher, err := crypto.NewCipher(crypto.Adiantum, bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x-journal")
	pf, err := openPass(path, vfs.OpenCreate|vfs.OpenMainJournal, cipher, ps)
	if err != nil {
		t.Fatal(err)
	}

	apply := func(buf []byte, off int64, data []byte) []byte {
		if end := off + int64(len(data)); int64(len(buf)) < end {
			buf = append(buf, make([]byte, end-int64(len(buf)))...)
		}
		copy(buf[off:], data)
		return buf
	}
	var want []byte
	for _, w := range []struct {
		off  int64
		data []byte
	}{
		{0, []byte(marker + " header at zero")}, // sub-page at start
		{100, bytes.Repeat([]byte("A"), 50)},    // sub-page RMW, same page
		{ps, bytes.Repeat([]byte("B"), ps)},     // a whole aligned page
		{ps*2 + 13, []byte(marker + " span")},   // mid-page in a fresh page
	} {
		if n, err := pf.WriteAt(w.data, w.off); err != nil || n != len(w.data) {
			t.Fatalf("WriteAt(%d) = (%d, %v)", w.off, n, err)
		}
		want = apply(want, w.off, w.data)
	}
	if err := pf.Sync(0); err != nil {
		t.Fatal(err)
	}

	got := make([]byte, len(want))
	if _, err := pf.ReadAt(got, 0); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("full round-trip mismatch")
	}
	sub := make([]byte, 60)
	if _, err := pf.ReadAt(sub, ps*2); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !bytes.Equal(sub, want[ps*2:ps*2+60]) {
		t.Fatal("sub-window read mismatch")
	}
	_ = pf.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(marker)) {
		t.Error("plaintext marker found in the encrypted journal at rest")
	}
}

// TestLiveEncryptionWAL drives an encrypted database in WAL mode end to end and
// checks the -wal on disk is ciphertext.
func TestLiveEncryptionWAL(t *testing.T) {
	const marker = "WAL_SECRET_MARKER_98765"
	path := filepath.Join(t.TempDir(), "enc-wal.dbz")
	opts := Options{Key: bytes.Repeat([]byte{5}, 32), Cipher: crypto.Adiantum}
	cfg := sqlite.Config{Path: path}
	cfg.Pragmas.JournalMode = sqlite.JournalWAL

	db, err := Open(cfg, opts)
	if err != nil {
		t.Fatalf("open encrypted WAL: %v", err)
	}
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, marker+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 50 {
		t.Fatalf("count = %d, err = %v", n, err)
	}

	// The -wal (if present) must be ciphertext.
	if wal, err := os.ReadFile(path + "-wal"); err == nil && len(wal) > 0 && bytes.Contains(wal, []byte(marker)) {
		t.Error("plaintext marker found in the -wal at rest")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen with the key: rows intact.
	db2, err := Open(cfg, opts)
	if err != nil {
		t.Fatalf("reopen encrypted WAL: %v", err)
	}
	if err := db2.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 50 {
		t.Fatalf("reopen count = %d, err = %v", n, err)
	}
	_ = db2.Close()
}

// TestLiveEncryptionRecipients is the headline multi-key case: a database
// encrypted to two recipients is opened independently by either one's identity
// (no shared secret), an unlisted identity is rejected, and the plaintext never
// hits disk.
func TestLiveEncryptionRecipients(t *testing.T) {
	const marker = "RECIPIENTS_SECRET_42"
	aliceR, aliceID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	bobR, bobID, err := keyring.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rcpt.dbz")

	db, err := Open(sqlite.Config{Path: path}, Options{Recipients: []keyring.Recipient{aliceR, bobR}})
	if err != nil {
		t.Fatalf("create to recipients: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, marker+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Either identity opens it independently.
	for name, id := range map[string]keyring.Identity{"alice": aliceID, "bob": bobID} {
		db, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{id}})
		if err != nil {
			t.Fatalf("open as %s: %v", name, err)
		}
		var n int
		err = db.QueryRow(`SELECT count(*) FROM t`).Scan(&n)
		_ = db.Close()
		if err != nil || n != 50 {
			t.Fatalf("open as %s: count=%d err=%v", name, n, err)
		}
	}

	// An unlisted identity, or none, cannot.
	_, eveID, _ := keyring.GenerateX25519()
	if db, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{eveID}}); err == nil {
		_ = db.Close()
		t.Error("open with an unlisted identity: want error")
	}
	if db, err := Open(sqlite.Config{Path: path}, Options{}); err == nil {
		_ = db.Close()
		t.Error("open with no identity: want error")
	}

	if raw, err := os.ReadFile(path); err == nil && bytes.Contains(raw, []byte(marker)) {
		t.Error("plaintext marker found in the encrypted file at rest")
	}
}

// TestRegistryKeyReuse verifies a second opener of an already-open encrypted
// database is rejected unless it holds the matching key — it cannot silently
// inherit the first opener's cipher through the shared container registry.
func TestRegistryKeyReuse(t *testing.T) {
	aliceR, aliceID, _ := keyring.GenerateX25519()
	_, eveID, _ := keyring.GenerateX25519()
	path := filepath.Join(t.TempDir(), "shared.dbz")

	db1, err := Open(sqlite.Config{Path: path}, Options{Recipients: []keyring.Recipient{aliceR}})
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	if _, err := db1.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}

	// A second open with no identity, or a non-recipient identity, must fail to
	// reach the shared container rather than inherit the live cipher.
	for name, opts := range map[string]Options{
		"no identity":    {},
		"wrong identity": {Identities: []keyring.Identity{eveID}},
	} {
		db, err := Open(sqlite.Config{Path: path}, opts)
		if err != nil {
			continue // rejected at open (the eager first connection) — good
		}
		var n int
		qerr := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n)
		_ = db.Close()
		if qerr == nil {
			t.Errorf("%s: want error accessing the shared encrypted container", name)
		}
	}

	// The right identity shares it.
	db2, err := Open(sqlite.Config{Path: path}, Options{Identities: []keyring.Identity{aliceID}})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db2.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Errorf("matching identity should share the container: %v", err)
	}
	_ = db2.Close()
}

// TestEmptyReadOnlyEncrypted verifies that opening an empty file read-only with
// a key errors rather than silently returning a plaintext container.
func TestEmptyReadOnlyEncrypted(t *testing.T) {
	const bs, ps = defaultBlockSize, defaultPageSize
	kc := keyConfig{cipher: crypto.Adiantum, rawKey: bytes.Repeat([]byte{1}, 32)}
	if _, err := newContainerOver(newCrashBacking(nil), true, bs, ps, CompressionDefault, kc); err == nil {
		t.Fatal("empty read-only open with a key: want error")
	}
}

// TestEncryptionCheckEnc pins the open-time key validation at the engine seam
// for the raw-key path (the typed errors don't survive SQLite's C-ABI open).
func TestEncryptionCheckEnc(t *testing.T) {
	const bs, ps = defaultBlockSize, defaultPageSize
	cb := newCrashBacking(nil)
	key := bytes.Repeat([]byte{7}, 32)
	withKey := keyConfig{cipher: crypto.Adiantum, rawKey: key}

	// Create an empty encrypted container (its commit records enc on disk).
	if _, err := newContainerOver(cb, false, bs, ps, CompressionDefault, withKey); err != nil {
		t.Fatalf("create encrypted container: %v", err)
	}

	// Reopen without a key → ErrEncrypted.
	if _, err := newContainerOver(cb, true, bs, ps, CompressionDefault, keyConfig{}); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("reopen without key = %v, want ErrEncrypted", err)
	}

	// Reopen with the wrong key bytes → ErrWrongKey (the directory canary fails),
	// even on this empty database.
	wrong := keyConfig{cipher: crypto.Adiantum, rawKey: bytes.Repeat([]byte{8}, 32)}
	if _, err := newContainerOver(cb, true, bs, ps, CompressionDefault, wrong); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("reopen with wrong key = %v, want ErrWrongKey", err)
	}

	// Reopen with the right key → succeeds.
	if _, err := newContainerOver(cb, true, bs, ps, CompressionDefault, withKey); err != nil {
		t.Fatalf("reopen with key: %v", err)
	}

	// A plaintext container rejects a key.
	cbPlain := newCrashBacking(nil)
	if _, err := newContainerOver(cbPlain, false, bs, ps, CompressionDefault, keyConfig{}); err != nil {
		t.Fatalf("create plaintext container: %v", err)
	}
	if _, err := newContainerOver(cbPlain, true, bs, ps, CompressionDefault, withKey); err == nil {
		t.Fatal("reopen plaintext with key: want error")
	}
}
