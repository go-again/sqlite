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

// TestEncryptionCheckEnc pins the open-time enc/key validation at the engine
// seam (the typed errors don't survive SQLite's C-ABI open path).
func TestEncryptionCheckEnc(t *testing.T) {
	cb := newCrashBacking(nil)
	key := bytes.Repeat([]byte{7}, 32)
	cipher, err := crypto.NewCipher(crypto.Adiantum, key)
	if err != nil {
		t.Fatal(err)
	}
	// Create an empty encrypted container (its commit records enc on disk).
	if _, err := newContainerOver(cb, false, defaultBlockSize, defaultPageSize, CompressionDefault, cipher, encAdiantum); err != nil {
		t.Fatalf("create encrypted container: %v", err)
	}

	// Reopen without a key → ErrEncrypted.
	if _, err := newContainerOver(cb, true, defaultBlockSize, defaultPageSize, CompressionDefault, nil, encNone); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("reopen without key = %v, want ErrEncrypted", err)
	}

	// Reopen with the wrong cipher kind → a mismatch error (not ErrEncrypted).
	xts, err := crypto.NewCipher(crypto.AESXTS, bytes.Repeat([]byte{9}, 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newContainerOver(cb, true, defaultBlockSize, defaultPageSize, CompressionDefault, xts, encAESXTS); err == nil {
		t.Fatal("reopen with wrong cipher kind: want error")
	}

	// Reopen with the right kind but wrong key bytes → ErrWrongKey (the
	// directory canary fails to decrypt), even on this empty database.
	wrongKey, err := crypto.NewCipher(crypto.Adiantum, bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newContainerOver(cb, true, defaultBlockSize, defaultPageSize, CompressionDefault, wrongKey, encAdiantum); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("reopen with wrong key = %v, want ErrWrongKey", err)
	}

	// Reopen with the right key → succeeds.
	cipher2, _ := crypto.NewCipher(crypto.Adiantum, key)
	if _, err := newContainerOver(cb, true, defaultBlockSize, defaultPageSize, CompressionDefault, cipher2, encAdiantum); err != nil {
		t.Fatalf("reopen with key: %v", err)
	}

	// And a plaintext container rejects a key.
	cbPlain := newCrashBacking(nil)
	if _, err := newContainerOver(cbPlain, false, defaultBlockSize, defaultPageSize, CompressionDefault, nil, encNone); err != nil {
		t.Fatalf("create plaintext container: %v", err)
	}
	if _, err := newContainerOver(cbPlain, true, defaultBlockSize, defaultPageSize, CompressionDefault, cipher, encAdiantum); err == nil {
		t.Fatal("reopen plaintext with key: want error")
	}
}
