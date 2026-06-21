package compress

// Increment-5b WAL tests: the live VFS implements vfs.ShmFile (ShmGroup), so a
// database opened with journal_mode=WAL coordinates through the dispatcher-owned
// shared-memory WAL index. The main DB stays compressed; the transient -wal
// frames are uncompressed and fold into compressed slots on checkpoint.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	sqlite "gosqlite.org"
)

func openLiveWAL(t *testing.T, path string) *sqlite.DB {
	t.Helper()
	db, err := Open(sqlite.Config{
		Path:         path,
		MaxOpenConns: 4,
		Pragmas:      sqlite.Pragmas{JournalMode: sqlite.JournalWAL},
	}, Options{})
	if err != nil {
		t.Fatalf("Open(WAL): %v", err)
	}
	return db
}

func TestLiveWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.dbz")

	db := openLiveWAL(t, path)
	var jm string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&jm); err != nil || jm != "wal" {
		t.Fatalf("journal_mode = (%q, %v), want wal — shm/WAL did not engage", jm, err)
	}
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	batchInsert(t, db, 800)

	// A TRUNCATE checkpoint folds every WAL frame into the compressed main DB.
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	var ic string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check = (%q, %v), want ok", ic, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// After checkpoint the at-rest main DB is our compressed container.
	head := make([]byte, len(superblockMagic))
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open at-rest: %v", err)
	}
	_, _ = f.ReadAt(head, 0)
	f.Close()
	if string(head) != superblockMagic {
		t.Fatalf("at-rest magic = %q, want %q (main DB not compressed)", head, superblockMagic)
	}

	// Reopen (still WAL) and confirm the data survived.
	db = openLiveWAL(t, path)
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 800 {
		t.Fatalf("reopened count = (%d, %v), want 800", n, err)
	}
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("reopened integrity_check = (%q, %v), want ok", ic, err)
	}
}

// TestLiveWALSharedReaders proves the dispatcher-owned shm group is shared: a
// second pool opening the same path sees rows the first pool committed under
// WAL, coordinated through the in-process shm lock table.
func TestLiveWALSharedReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.dbz")

	writer := openLiveWAL(t, path)
	defer writer.Close()
	if _, err := writer.Exec(`CREATE TABLE t (v INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := writer.Exec(`INSERT INTO t VALUES (10), (20), (30)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reader := openLiveWAL(t, path)
	defer reader.Close()
	var sum int
	if err := reader.QueryRow(`SELECT coalesce(sum(v),0) FROM t`).Scan(&sum); err != nil {
		t.Fatalf("reader query: %v", err)
	}
	if sum != 60 {
		t.Fatalf("reader sum = %d, want 60 (shm not shared across pools?)", sum)
	}
}

// TestLiveWALConcurrent is the load-bearing WAL check (run under -race): one
// writer pool and several reader pools hammer a shared WAL database. WAL's
// invariant is that readers never block the writer and vice versa, so none
// should error and each reader's view of the row count must climb monotonically.
func TestLiveWALConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "walrace.dbz")

	writer := openLiveWAL(t, path)
	defer writer.Close()
	if _, err := writer.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	const writes, readers = 200, 4
	var wg sync.WaitGroup

	wg.Go(func() {
		for range writes {
			if _, err := writer.Exec(`INSERT INTO t DEFAULT VALUES`); err != nil {
				t.Errorf("insert: %v", err)
				return
			}
		}
	})

	for id := range readers {
		rd := openLiveWAL(t, path)
		wg.Go(func() {
			defer rd.Close()
			last := 0
			for range writes {
				var n int
				if err := rd.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
					t.Errorf("reader %d: %v", id, err)
					return
				}
				if n < last {
					t.Errorf("reader %d saw count regress %d → %d", id, last, n)
					return
				}
				last = n
			}
		})
	}
	wg.Wait()

	var n int
	if err := writer.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != writes {
		t.Fatalf("final count = (%d, %v), want %d", n, err, writes)
	}
}
