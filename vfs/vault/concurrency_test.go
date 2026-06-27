package vault

// Increment-5a concurrency tests: multiple pooled connections share one
// container and coordinate through the VFS's in-process advisory locks (many
// readers, one writer). Run these under -race.

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	sqlite "gosqlite.org"
)

func TestLiveConcurrentReadersAndWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.dbz")

	db, err := Open(sqlite.Config{Path: path, MaxOpenConns: 4}, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY, v INTEGER)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	const seed, total = 50, 300
	for i := range seed {
		if _, err := db.Exec(`INSERT INTO t (k, v) VALUES (?, ?)`, i, i); err != nil {
			t.Fatalf("seed insert: %v", err)
		}
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Concurrent readers run continuously until the writer finishes.
	for range 3 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				var n, sum int
				if err := db.QueryRow(`SELECT count(*), coalesce(sum(v),0) FROM t`).Scan(&n, &sum); err != nil {
					t.Errorf("reader: %v", err)
					return
				}
			}
		})
	}

	// One writer appends the rest, then signals the readers to stop.
	wg.Go(func() {
		defer close(stop)
		for i := seed; i < total; i++ {
			if _, err := db.Exec(`INSERT INTO t (k, v) VALUES (?, ?)`, i, i); err != nil {
				t.Errorf("writer: %v", err)
				return
			}
		}
	})
	wg.Wait()

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != total {
		t.Fatalf("final count = (%d, %v), want %d", n, err, total)
	}
	var ic string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check = (%q, %v), want ok", ic, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen confirms the concurrent load is durable on disk.
	db = openLive(t, path, Options{})
	defer db.Close()
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != total {
		t.Fatalf("reopened count = (%d, %v), want %d", n, err, total)
	}
}

func TestLiveConcurrentWritersSerialize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writers.dbz")

	db, err := Open(sqlite.Config{
		Path:         path,
		MaxOpenConns: 4,
		Pragmas:      sqlite.Pragmas{BusyTimeout: 30 * time.Second},
	}, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Several goroutines each commit a transaction of a disjoint key range
	// concurrently; the advisory lock serializes the write transactions, so every
	// insert must land exactly once and the database stays consistent.
	const writers, each = 4, 100
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			tx, err := db.Begin()
			if err != nil {
				t.Errorf("writer %d begin: %v", w, err)
				return
			}
			for i := range each {
				if _, err := tx.Exec(`INSERT INTO t (k) VALUES (?)`, w*each+i); err != nil {
					t.Errorf("writer %d: %v", w, err)
					_ = tx.Rollback()
					return
				}
			}
			if err := tx.Commit(); err != nil {
				t.Errorf("writer %d commit: %v", w, err)
			}
		})
	}
	wg.Wait()

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != writers*each {
		t.Fatalf("count = (%d, %v), want %d", n, err, writers*each)
	}
	var ic string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check = (%q, %v), want ok", ic, err)
	}
}

// TestReclaimConcurrentWithWrites drives the online reclaim ops (CompactOnline /
// Trim / ReclaimableBytes) in a loop while two goroutines churn the database, under
// -race: the ops pin the container through the registry and release the write lock
// between batches, so this exercises the pin/lock interplay against live writes. The
// database must stay consistent.
func TestReclaimConcurrentWithWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reclaim-writes.dbz")
	db, err := Open(sqlite.Config{
		Path:         path,
		MaxOpenConns: 4,
		Pragmas:      sqlite.Pragmas{JournalMode: sqlite.JournalWAL, BusyTimeout: 30 * time.Second},
	}, Options{PageSize: 8192})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY, v BLOB)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	blob := make([]byte, 2048)
	for i := range 300 {
		if _, err := db.Exec(`INSERT INTO t VALUES (?, ?)`, i, blob); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := range 2 {
		base := 1000 + w*1_000_000
		wg.Go(func() {
			for i := base; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := db.Exec(`INSERT INTO t VALUES (?, ?)`, i, blob); err != nil {
					return // a busy/closed error is fine; the reclaimer drives termination
				}
				_, _ = db.Exec(`DELETE FROM t WHERE k = ?`, i-3) // churn → freed pages
			}
		})
	}
	// The reclaimer runs a bounded number of passes, then stops the writers.
	wg.Go(func() {
		defer close(stop)
		for range 60 {
			_, _ = CompactOnline(path, 0, nil)
			_, _ = Trim(path, 0)
			_, _ = ReclaimableBytes(path)
		}
	})
	wg.Wait()

	var ic string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check = (%q, %v), want ok", ic, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestReclaimRacesClose runs reclaim ops on a loop while the database is Closed out
// from under them, under -race. The registry refcount must keep a pinned container
// alive for the in-flight op; once Closed, later calls return a "no open database"
// error rather than panicking or racing the backing teardown.
func TestReclaimRacesClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reclaim-close.dbz")
	db, err := Open(sqlite.Config{Path: path, MaxOpenConns: 2}, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY, v BLOB)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	blob := make([]byte, 2048)
	for i := range 200 {
		if _, err := db.Exec(`INSERT INTO t VALUES (?, ?)`, i, blob); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 300 {
			_, _ = CompactOnline(path, 0, nil) // after Close: returns an error, never panics
			_, _ = ReclaimableBytes(path)
		}
	})
	if err := db.Close(); err != nil { // races the reclaim loop above
		t.Fatalf("close: %v", err)
	}
	wg.Wait()
}
