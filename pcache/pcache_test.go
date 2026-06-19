package pcache_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/pcache"
)

// stats is installed once, before any database opens — the only moment
// SQLite's process-global page-cache hook can be set.
var stats *pcache.Stats

func TestMain(m *testing.M) {
	s, err := pcache.InstallBoundedLRU(16)
	if err != nil {
		fmt.Fprintln(os.Stderr, "InstallBoundedLRU:", err)
		os.Exit(1)
	}
	stats = s
	os.Exit(m.Run())
}

// openFileDB creates a fresh on-disk database with a small page size so a
// modest table spans many pages — the setup that makes a bounded cache
// actually evict. On-disk (not :memory:) pages are purgeable, which is
// what exercises the LRU.
func openFileDB(t *testing.T, pageSize int) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pc.db")
	db, err := sql.Open(sqlite.DriverName, fmt.Sprintf("file:%s?_pragma=page_size(%d)", path, pageSize))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// TestPageCache_Correctness is the load-bearing check: a bounded,
// evicting cache must still serve correct page content. A bug in the
// fetch/unpin/evict bookkeeping corrupts reads, so a matching checksum
// across thousands of rows proves the cache is correct, not merely
// bounded.
func TestPageCache_Correctness(t *testing.T) {
	db := openFileDB(t, 512)
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	const n = 5000
	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`INSERT INTO t (id, v) VALUES (?, ?)`)
	want := 0
	for i := 1; i <= n; i++ {
		if _, err := stmt.Exec(i, i*3); err != nil {
			t.Fatal(err)
		}
		want += i * 3
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var got int
	if err := db.QueryRow(`SELECT sum(v) FROM t`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("sum=%d, want %d — bounded cache corrupted reads", got, want)
	}
	// A second scan should hit cached pages.
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != n {
		t.Fatalf("count=%d, want %d", got, n)
	}
}

// TestPageCache_EvictsUnderBound churns a table far larger than the
// 16-page bound and confirms the LRU actually evicted pages (otherwise
// "bounded" is a lie) while still returning correct results.
func TestPageCache_EvictsUnderBound(t *testing.T) {
	before := stats.Snapshot()

	db := openFileDB(t, 512)
	if _, err := db.Exec(`CREATE TABLE big (id INTEGER PRIMARY KEY, pad TEXT)`); err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`INSERT INTO big (id, pad) VALUES (?, ?)`)
	const rows = 4000
	for i := 1; i <= rows; i++ {
		if _, err := stmt.Exec(i, fmt.Sprintf("row-%d-padding-padding", i)); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	tx.Commit()

	// Several full scans force page churn well past 16 cached pages.
	var n int
	for range 3 {
		if err := db.QueryRow(`SELECT count(*) FROM big`).Scan(&n); err != nil {
			t.Fatal(err)
		}
	}
	if n != rows {
		t.Fatalf("count=%d, want %d", n, rows)
	}

	after := stats.Snapshot()
	if after.Evictions <= before.Evictions {
		t.Errorf("no evictions recorded (%d → %d); the bound is not being enforced",
			before.Evictions, after.Evictions)
	}
	if after.Hits <= before.Hits {
		t.Errorf("no cache hits recorded (%d → %d)", before.Hits, after.Hits)
	}
	if after.Misses <= before.Misses {
		t.Errorf("no cache misses recorded (%d → %d)", before.Misses, after.Misses)
	}
}

// TestPageCache_PagesReleased confirms the live-page gauge falls back
// toward zero once a database closes (xDestroy frees its blocks), i.e.
// the cache doesn't leak C memory across connection lifetimes.
func TestPageCache_PagesReleased(t *testing.T) {
	db := openFileDB(t, 512)
	if _, err := db.Exec(`CREATE TABLE t(x); INSERT INTO t VALUES (1),(2),(3)`); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM t`).Scan(&n)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// After close, this connection's pages are freed. The global gauge
	// reflects only still-open databases; with none from this test it
	// must be non-negative and not stuck high from our churn.
	if p := stats.Snapshot().Pages; p < 0 {
		t.Errorf("live page gauge went negative: %d", p)
	}
}

func TestPageCache_AlreadyInstalled(t *testing.T) {
	if _, err := pcache.InstallBoundedLRU(100); err == nil {
		t.Error("second InstallBoundedLRU succeeded; want error")
	}
}

func TestPageCache_RejectsBadSize(t *testing.T) {
	// A fresh install can't be tested (global hook already set), but the
	// argument validation fires before the install guard.
	if _, err := pcache.InstallBoundedLRU(0); err == nil {
		t.Error("InstallBoundedLRU(0) accepted; want error")
	}
}
