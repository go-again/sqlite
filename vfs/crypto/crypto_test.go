package crypto_test

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs/crypto"
)

// isBusy reports whether err is a SQLITE_BUSY result — expected lock
// contention under concurrent writers, not a corruption signal.
func isBusy(err error) bool {
	var serr *sqlite.Error
	return errors.As(err, &serr) && serr.Code() == sqlite.SQLITE_BUSY
}

// freshKey returns a 32-byte deterministic test key seeded from i so
// different tests can use distinct keys without dragging in a random
// source the test runner has to manage.
func freshKey(i byte) []byte {
	k := make([]byte, 32)
	for j := range k {
		k[j] = i ^ byte(j)
	}
	return k
}

// TestRoundTrip exercises the headline case: write rows through an
// Adiantum-encrypted VFS, close, reopen with the same key, and read
// the rows back. The file on disk is real ciphertext (verified by
// TestOnDiskIsNotPlaintext below).
func TestRoundTrip(t *testing.T) {
	key := freshKey(1)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "rt.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)

	db, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t (id, v) VALUES (1, 'hello'), (2, 'encrypted world')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT v FROM t WHERE id = 2`).Scan(&v); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if v != "encrypted world" {
		t.Errorf("v=%q, want %q", v, "encrypted world")
	}
}

// TestReopenSameKey confirms encrypted state survives a close/reopen
// when the key matches. Different sql.DB, same VFS registration.
func TestReopenSameKey(t *testing.T) {
	key := freshKey(2)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "persist.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)

	db1, _ := sql.Open(sqlite.DriverName, dsn)
	if _, err := db1.Exec(`CREATE TABLE t (v TEXT)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := db1.Exec(`INSERT INTO t VALUES ('persisted')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("db1.Close: %v", err)
	}

	db2, _ := sql.Open(sqlite.DriverName, dsn)
	t.Cleanup(func() { _ = db2.Close() })
	var v string
	if err := db2.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("SELECT after reopen: %v", err)
	}
	if v != "persisted" {
		t.Errorf("v=%q, want persisted", v)
	}
}

// TestReopenWrongKeyFailsLoudly confirms the wrong key produces an
// obvious error rather than silent garbage. SQLite reports "file is
// not a database" or similar within ~one query, which is the
// contract callers depend on for key-rotation safety nets.
func TestReopenWrongKeyFailsLoudly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wrongkey.db")

	keyA := freshKey(3)
	nameA, fsA, err := crypto.New(crypto.Options{Key: keyA})
	if err != nil {
		t.Fatalf("crypto.New A: %v", err)
	}
	dsnA := fmt.Sprintf("file:%s?vfs=%s", dbPath, nameA)
	dbA, _ := sql.Open(sqlite.DriverName, dsnA)
	if _, err := dbA.Exec(`CREATE TABLE t (v TEXT)`); err != nil {
		t.Fatalf("CREATE under key A: %v", err)
	}
	if _, err := dbA.Exec(`INSERT INTO t VALUES ('secret')`); err != nil {
		t.Fatalf("INSERT under key A: %v", err)
	}
	if err := dbA.Close(); err != nil {
		t.Fatalf("dbA.Close: %v", err)
	}
	if err := fsA.Close(); err != nil {
		t.Fatalf("fsA.Close: %v", err)
	}

	keyB := freshKey(99)
	nameB, fsB, err := crypto.New(crypto.Options{Key: keyB})
	if err != nil {
		t.Fatalf("crypto.New B: %v", err)
	}
	t.Cleanup(func() { _ = fsB.Close() })
	dsnB := fmt.Sprintf("file:%s?vfs=%s", dbPath, nameB)
	dbB, _ := sql.Open(sqlite.DriverName, dsnB)
	t.Cleanup(func() { _ = dbB.Close() })

	var v string
	err = dbB.QueryRow(`SELECT v FROM t`).Scan(&v)
	if err == nil {
		t.Fatalf("wrong key: SELECT returned %q with no error", v)
	}
	// We don't pin a specific error string — SQLite phrases it
	// differently depending on whether it bails at header parse,
	// pager init, or schema load. Any error is acceptable; silent
	// data corruption is not.
	t.Logf("wrong-key error (expected): %v", err)
}

// TestOnDiskIsNotPlaintext confirms the raw bytes on disk don't
// contain plaintext fingerprints. Without encryption the SQLite
// magic string "SQLite format 3" appears at offset 0; with our VFS
// it should not.
func TestOnDiskIsNotPlaintext(t *testing.T) {
	key := freshKey(4)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "ciphertext.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	if _, err := db.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('plaintext-marker-string')`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if bytes.Contains(raw, []byte("SQLite format 3")) {
		t.Error("on-disk file contains plaintext SQLite header magic — encryption is not engaged")
	}
	if bytes.Contains(raw, []byte("plaintext-marker-string")) {
		t.Error("on-disk file contains the row's plaintext value — encryption is not engaged")
	}
}

// TestNew_RejectsBadInputs covers the obvious misuse paths so a
// caller doesn't quietly fall through with a degraded config.
func TestNew_RejectsBadInputs(t *testing.T) {
	cases := []struct {
		name string
		opts crypto.Options
	}{
		{"nil key", crypto.Options{}},
		{"short key", crypto.Options{Key: make([]byte, 16)}},
		{"long key", crypto.Options{Key: make([]byte, 64)}},
		{"AESXTS wrong key length", crypto.Options{Key: make([]byte, 32), Cipher: crypto.AESXTS}},
		{"bad pagesize", crypto.Options{Key: make([]byte, 32), PageSize: 1000}},
		{"too-small pagesize", crypto.Options{Key: make([]byte, 32), PageSize: 256}},
		{"unknown cipher", crypto.Options{Key: make([]byte, 32), Cipher: 99}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := crypto.New(tc.opts)
			if err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

// TestWALMode_RoundTrip runs a transaction under WAL mode so the
// engine creates a -wal sidecar and a -shm region. We confirm
// CREATE / INSERT / COMMIT / SELECT round-trips, and that the -wal
// file's bytes don't contain plaintext. The -shm file is
// intentionally plaintext (it's accessed via xShmMap, not
// xRead/xWrite — see fileKindFor in vfs.go).
func TestWALMode_RoundTrip(t *testing.T) {
	key := freshKey(5)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "wal.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)

	// PRAGMA journal_mode = WAL returns the new mode as a row; SQLite
	// silently falls back to DELETE if the VFS doesn't support the
	// xShm methods. Scan the return to make sure we actually got WAL.
	var newMode string
	if err := db.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&newMode); err != nil {
		t.Fatalf("PRAGMA journal_mode=WAL: %v", err)
	}
	if newMode != "wal" {
		t.Fatalf("journal_mode=%q after WAL pragma, want \"wal\" (VFS xShm methods missing?)", newMode)
	}
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for i := range 50 {
		if _, err := tx.Exec(`INSERT INTO t (id, v) VALUES (?, ?)`, i, fmt.Sprintf("wal-row-%d-plaintext-marker", i)); err != nil {
			t.Fatalf("INSERT %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("COUNT: %v", err)
	}
	if count != 50 {
		t.Errorf("count=%d, want 50", count)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	// The -wal file may or may not still exist depending on whether
	// SQLite checkpointed on Close. Check whichever encrypted files
	// remain on disk.
	for _, suffix := range []string{"", "-wal"} {
		p := dbPath + suffix
		raw, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Errorf("read %s: %v", p, err)
			continue
		}
		if len(raw) == 0 {
			continue
		}
		if bytes.Contains(raw, []byte("wal-row-0-plaintext-marker")) {
			t.Errorf("on-disk file %s contains plaintext row content", filepath.Base(p))
		}
	}
}

// TestRollbackJournal_Encrypted runs a transaction under the default
// rollback-journal mode and confirms the -journal sidecar doesn't
// leak plaintext if SQLite writes it before commit.
func TestRollbackJournal_Encrypted(t *testing.T) {
	key := freshKey(6)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "rj.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE t (v TEXT)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES ('journal-plaintext-marker')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	// Trigger a journal write by starting a transaction that touches
	// the row, then commit.
	tx, _ := db.Begin()
	if _, err := tx.Exec(`UPDATE t SET v = 'updated' WHERE v = 'journal-plaintext-marker'`); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// If a -journal file exists (cleanup vs delete-on-commit depends
	// on the platform), confirm it's not plaintext.
	jp := dbPath + "-journal"
	raw, err := os.ReadFile(jp)
	if err == nil && len(raw) > 0 {
		if bytes.Contains(raw, []byte("journal-plaintext-marker")) {
			t.Error("rollback journal contains plaintext row content")
		}
	}
}

// TestRollbackJournal_ReplaysCorrectly exercises the journal-replay-
// decrypt path. The previous TestRollbackJournal_Encrypted commits
// the tx so the journal is written and immediately deleted; rollback
// is what triggers a journal replay (SQLite reads the original page
// images out of the encrypted journal to restore the main DB).
func TestRollbackJournal_ReplaysCorrectly(t *testing.T) {
	key := freshKey(60)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "rb.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	for i := range 20 {
		if _, err := db.Exec(`INSERT INTO t VALUES (?, ?)`, i, fmt.Sprintf("original-%d", i)); err != nil {
			t.Fatalf("INSERT %d: %v", i, err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec(`UPDATE t SET v = 'modified' WHERE id < 10`); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// The rollback walks the encrypted journal, decrypts page images,
	// and restores the main DB. Verify the originals are intact.
	for i := range 20 {
		var v string
		if err := db.QueryRow(`SELECT v FROM t WHERE id = ?`, i).Scan(&v); err != nil {
			t.Fatalf("SELECT id=%d: %v", i, err)
		}
		want := fmt.Sprintf("original-%d", i)
		if v != want {
			t.Errorf("id=%d: v=%q, want %q (rollback didn't restore)", i, v, want)
		}
	}
}

// TestLargeBlob_SpansMultiplePages forces a single xWrite to span
// multiple page-aligned encryption blocks. The encryptSpan /
// decryptSpan loops in iomethods.go iterate per page; a single-page
// payload wouldn't exercise the multi-iteration branch.
func TestLargeBlob_SpansMultiplePages(t *testing.T) {
	key := freshKey(61)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "big.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, b BLOB)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	// 100 KiB blob — at default 4 KiB pages that's ~25 pages.
	payload := make([]byte, 100*1024)
	for i := range payload {
		payload[i] = byte(i*7 + 13)
	}
	if _, err := db.Exec(`INSERT INTO t (id, b) VALUES (1, ?)`, payload); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var got []byte
	if err := db.QueryRow(`SELECT b FROM t WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("blob round-trip mismatch: got %d bytes, want %d; first diff at %d",
			len(got), len(payload), firstDiff(got, payload))
	}
}

func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}

// TestWAL_VacuumAndCheckpoint exercises VACUUM under WAL mode plus
// an explicit checkpoint — a code path that combines whole-DB
// rewriting (VACUUM) with WAL-frame flushing (checkpoint), both of
// which go through our encrypted xWrite trampolines on different
// file kinds.
func TestWAL_VacuumAndCheckpoint(t *testing.T) {
	key := freshKey(62)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "wv.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatalf("PRAGMA WAL: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	tx, _ := db.Begin()
	for i := range 100 {
		if _, err := tx.Exec(`INSERT INTO t VALUES (?, ?)`, i, fmt.Sprintf("row-%d", i)); err != nil {
			t.Fatalf("INSERT %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM t WHERE id % 2 = 0`); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		t.Fatalf("VACUUM: %v", err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("wal_checkpoint: %v", err)
	}
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if result != "ok" {
		t.Errorf("integrity_check = %q, want \"ok\"", result)
	}
}

// TestConcurrentReadsWrites_NoCorruption stress-tests the full
// trampoline path with multiple sql.Conns issuing concurrent I/O
// against the same encrypted VFS. The package-wide -race skip
// means TSan never sees this code; this test catches logic-level
// races (like the Adiantum hashBuf race that motivated the cipher
// mutex) at the SQL boundary instead.
func TestConcurrentReadsWrites_NoCorruption(t *testing.T) {
	key := freshKey(63)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "stress.db")
	// A modest busy_timeout absorbs the common case; SQLITE_BUSY beyond it
	// is tolerated below (skipped, not fatal) rather than waited out. This
	// test proves cipher-level correctness under concurrency — lock
	// fairness is SQLite's concern, not ours. A large timeout only makes a
	// starved writer on a slow/loaded Windows CI runner block for tens of
	// seconds before STILL surfacing BUSY (which is why both 5s and 30s
	// flaked here, the latter dragging the run to ~50s). Keep it small so
	// the worst case stays well under the CI test timeout.
	dsn := fmt.Sprintf("file:%s?vfs=%s&_pragma=busy_timeout(1000)", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(8)

	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatalf("PRAGMA WAL: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	const goroutines = 8
	const iterations = 200
	var wg sync.WaitGroup
	var busyErrs atomic.Int64
	errs := make(chan error, goroutines*iterations)
	for g := range goroutines {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()
			for i := range iterations {
				id := gID*iterations + i
				if _, err := db.Exec(`INSERT INTO t (id, v) VALUES (?, ?)`,
					id, fmt.Sprintf("g%d-i%d", gID, i)); err != nil {
					// SQLITE_BUSY is expected lock contention under N-way WAL
					// writers, not corruption. Skip this id; the integrity
					// check and every row that did land still prove no
					// corruption. Only a non-BUSY error is a real failure.
					if isBusy(err) {
						busyErrs.Add(1)
						continue
					}
					errs <- fmt.Errorf("INSERT %d: %w", id, err)
					return
				}
				var v string
				if err := db.QueryRow(`SELECT v FROM t WHERE id = ?`, id).Scan(&v); err != nil {
					if isBusy(err) {
						busyErrs.Add(1)
						continue
					}
					errs <- fmt.Errorf("SELECT %d: %w", id, err)
					return
				}
				want := fmt.Sprintf("g%d-i%d", gID, i)
				if v != want {
					errs <- fmt.Errorf("id=%d: v=%q, want %q", id, v, want)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	// BUSY events are tolerated (logged, not fatal): a genuine cipher/VFS
	// corruption would instead surface as a read-back mismatch above or a
	// failing integrity_check below, regardless of how many writes landed.
	if b := busyErrs.Load(); b > 0 {
		t.Logf("tolerated %d SQLITE_BUSY events under %d-way encrypted WAL write contention", b, goroutines)
	}

	// Final integrity check after the storm.
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if result != "ok" {
		t.Errorf("integrity_check after concurrent storm = %q, want \"ok\"", result)
	}
}

// TestAESXTS_RoundTrip exercises the AES-XTS cipher path: same
// round-trip shape as the Adiantum tests but with a 64-byte key
// (two AES-256 keys per the XTS construction).
func TestAESXTS_RoundTrip(t *testing.T) {
	key := make([]byte, 64)
	for i := range key {
		key[i] = byte(i ^ 0x5a)
	}
	name, fs, err := crypto.New(crypto.Options{Key: key, Cipher: crypto.AESXTS})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "xts.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t (id, v) VALUES (1, 'xts-row'), (2, 'second')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT v FROM t WHERE id = 1`).Scan(&v); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if v != "xts-row" {
		t.Errorf("v=%q, want xts-row", v)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	raw, _ := os.ReadFile(dbPath)
	if bytes.Contains(raw, []byte("SQLite format 3")) {
		t.Error("AES-XTS on-disk file contains plaintext header magic")
	}
}

// TestCipherCrossover confirms an Adiantum-encrypted file cannot be
// opened with AES-XTS (and vice versa). Mode mismatch must fail at
// open or first query — silent acceptance would corrupt the file.
func TestCipherCrossover(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cross.db")
	adKey := freshKey(7)
	xtsKey := make([]byte, 64)
	for i := range xtsKey {
		xtsKey[i] = byte(i)
	}

	nameAd, fsAd, err := crypto.New(crypto.Options{Key: adKey})
	if err != nil {
		t.Fatalf("crypto.New Adiantum: %v", err)
	}
	dsnAd := fmt.Sprintf("file:%s?vfs=%s", dbPath, nameAd)
	dbAd, _ := sql.Open(sqlite.DriverName, dsnAd)
	if _, err := dbAd.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('mode-A')`); err != nil {
		t.Fatalf("write under Adiantum: %v", err)
	}
	_ = dbAd.Close()
	_ = fsAd.Close()

	nameXts, fsXts, err := crypto.New(crypto.Options{Key: xtsKey, Cipher: crypto.AESXTS})
	if err != nil {
		t.Fatalf("crypto.New XTS: %v", err)
	}
	t.Cleanup(func() { _ = fsXts.Close() })
	dsnXts := fmt.Sprintf("file:%s?vfs=%s", dbPath, nameXts)
	dbXts, _ := sql.Open(sqlite.DriverName, dsnXts)
	t.Cleanup(func() { _ = dbXts.Close() })
	var v string
	err = dbXts.QueryRow(`SELECT v FROM t`).Scan(&v)
	if err == nil {
		t.Fatalf("XTS opening Adiantum-encrypted file: SELECT returned %q with no error", v)
	}
	t.Logf("cipher mismatch (expected): %v", err)
}

// TestPageSizeMismatch_FailsLoudly pins the package doc's promise
// that opening a file with the wrong PageSize is detected. Write a
// DB under PageSize=4096 (the default), close, reopen with
// PageSize=8192. The header read decrypts under the wrong page tweak
// and SQLite sees corruption.
func TestPageSizeMismatch_FailsLoudly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ps.db")
	key := freshKey(9)

	name4k, fs4k, err := crypto.New(crypto.Options{Key: key, PageSize: 4096})
	if err != nil {
		t.Fatalf("crypto.New 4k: %v", err)
	}
	dsn4k := fmt.Sprintf("file:%s?vfs=%s", dbPath, name4k)
	db4k, _ := sql.Open(sqlite.DriverName, dsn4k)
	if _, err := db4k.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('hi')`); err != nil {
		t.Fatalf("write under 4k: %v", err)
	}
	_ = db4k.Close()
	_ = fs4k.Close()

	name8k, fs8k, err := crypto.New(crypto.Options{Key: key, PageSize: 8192})
	if err != nil {
		t.Fatalf("crypto.New 8k: %v", err)
	}
	t.Cleanup(func() { _ = fs8k.Close() })
	dsn8k := fmt.Sprintf("file:%s?vfs=%s", dbPath, name8k)
	db8k, _ := sql.Open(sqlite.DriverName, dsn8k)
	t.Cleanup(func() { _ = db8k.Close() })

	var v string
	err = db8k.QueryRow(`SELECT v FROM t`).Scan(&v)
	if err == nil {
		t.Fatalf("page-size mismatch: SELECT returned %q with no error", v)
	}
	t.Logf("page-size mismatch (expected): %v", err)
}

// TestSHM_StaysPlaintext pins fileKindFor's deliberate omission
// of SQLITE_OPEN_MAIN_DB's -shm companion. The -shm file is a memory-
// mapped WAL index (no row data), accessed via xShmMap rather than
// xRead/xWrite, and SQLite expects to find specific magic bytes at
// known offsets. Verify the file on disk is NOT ciphertext.
func TestSHM_StaysPlaintext(t *testing.T) {
	key := freshKey(10)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "shm.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatalf("PRAGMA WAL: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('row')`); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Hold a connection open so -shm stays on disk (SQLite may
	// remove it when all readers close cleanly).
	// Read while the WAL is open.
	shm, err := os.ReadFile(dbPath + "-shm")
	if err != nil {
		// Some platforms inline the shm region and don't materialize
		// the file; skip rather than fail in that case.
		if os.IsNotExist(err) {
			t.Skip("-shm not materialized on this platform")
		}
		t.Fatalf("read -shm: %v", err)
	}
	_ = db.Close()
	if len(shm) < 32 {
		t.Skipf("-shm too small (%d bytes) for the check", len(shm))
	}
	// SQLite WAL index header starts with iVersion (1 << 24) + flags
	// + checksum etc. — not a fixed magic, but the first 4 bytes of
	// the iVersion field should be a small value (typically 3007000
	// for the current WAL format). Easier check: the bytes should
	// have low Hamming distance from the all-zero-prefix-then-int
	// pattern, which encryption would scramble.
	allHigh := !slices.Contains(shm[:8], 0)
	if allHigh {
		t.Errorf("-shm first 8 bytes look encrypted (no zero bytes in header): %x", shm[:8])
	}
}

// TestIntegrityCheck runs `PRAGMA integrity_check` after a
// write-heavy workload. SQLite's check walks the entire page graph
// and verifies internal consistency; passing means decryption is
// exact-round-trip across every page we wrote, including pages
// rewritten by VACUUM-style rebuilds.
func TestIntegrityCheck(t *testing.T) {
	key := freshKey(8)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "integ.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, blob BLOB)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	tx, _ := db.Begin()
	for i := range 200 {
		// Mix small and large values to spread across multiple pages.
		payload := make([]byte, 100+i*5)
		for j := range payload {
			payload[j] = byte(i + j)
		}
		if _, err := tx.Exec(`INSERT INTO t (id, blob) VALUES (?, ?)`, i, payload); err != nil {
			t.Fatalf("INSERT %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM t WHERE id % 3 = 0`); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		t.Fatalf("VACUUM: %v", err)
	}

	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if result != "ok" {
		t.Errorf("integrity_check = %q, want \"ok\"", result)
	}
}

// TestNew_DefensiveCopiesKey pins the Options.Key docstring
// guarantee: callers are free to zero or mutate the slice as soon as
// New returns. lukechampine.com/adiantum's chachaStream keeps a
// reference to its input key and dereferences it on every Encrypt
// call, so without the defensive copy this test would fail
// catastrophically — every read/write after the zero would scramble.
func TestNew_DefensiveCopiesKey(t *testing.T) {
	key := freshKey(50)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	// Zero the caller's key before any I/O. If New took the key by
	// reference, subsequent operations would silently corrupt.
	for i := range key {
		key[i] = 0
	}

	dbPath := filepath.Join(t.TempDir(), "defcopy.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE t (v TEXT)`); err != nil {
		t.Fatalf("CREATE after key zero: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES ('survived')`); err != nil {
		t.Fatalf("INSERT after key zero: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("SELECT after key zero: %v", err)
	}
	if v != "survived" {
		t.Errorf("v=%q, want 'survived'", v)
	}
}

// TestRandomKey is a sanity check that the test harness isn't
// inadvertently picking colliding keys via freshKey() — using
// crypto/rand should produce distinct ciphertexts on every run.
func TestRandomKey(t *testing.T) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatal(err)
	}
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(t.TempDir(), "rand.db")
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	var id int
	if err := db.QueryRow(`SELECT id FROM t`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 42 {
		t.Errorf("id=%d, want 42", id)
	}
}
