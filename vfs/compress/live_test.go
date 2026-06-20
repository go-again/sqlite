package compress

// Increment-2 end-to-end tests for the live compressing VFS: a real database
// queried while it stays compressed on disk, durable per transaction.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlite "gosqlite.org"
)

// openLive is a test helper that opens path through the live VFS and fails the
// test on error.
func openLive(t *testing.T, path string, opts Options) *sqlite.DB {
	t.Helper()
	db, err := OpenLive(sqlite.Config{Path: path}, opts)
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	return db
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	return fi.Size()
}

func TestLiveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.dbz")

	db := openLive(t, path, Options{})
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Highly compressible, repetitive rows across several transactions.
	const rows = 500
	for i := range rows {
		v := strings.Repeat(fmt.Sprintf("row-%d ", i%10), 20)
		if _, err := db.Exec(`INSERT INTO t (v) VALUES (?)`, v); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// Confirm the page size took effect (the VFS and SQLite must agree).
	var ps int
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&ps); err != nil || ps != defaultPageSize {
		t.Fatalf("page_size = (%d, %v), want %d", ps, err, defaultPageSize)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The at-rest file is our container, not a raw SQLite database.
	head := make([]byte, len(superblockMagic))
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open at-rest: %v", err)
	}
	_, _ = f.ReadAt(head, 0)
	f.Close()
	if string(head) != superblockMagic {
		t.Fatalf("at-rest magic = %q, want %q (file is not a compressed container)", head, superblockMagic)
	}

	// Reopen through the live VFS: data persists and the database verifies.
	db = openLive(t, path, Options{})
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != rows {
		t.Fatalf("count = (%d, %v), want (%d, nil)", n, err, rows)
	}
	var ic string
	if err := db.QueryRowContext(context.Background(), `PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check = (%q, %v), want (ok, nil)", ic, err)
	}

	// Compression is real: the on-disk container is far smaller than the
	// logical database it represents.
	var pageCount, pageSize int64
	if err := db.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	logical := pageCount * pageSize
	physical := fileSize(t, path)
	if physical >= logical {
		t.Fatalf("at-rest %d bytes not smaller than logical %d bytes", physical, logical)
	}
	t.Logf("logical=%d physical=%d ratio=%.1f%%", logical, physical, 100*float64(physical)/float64(logical))
}

func TestLiveUpdatesAndDeletesPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.dbz")

	db := openLive(t, path, Options{Level: CompressionBest})
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := range 200 {
		if _, err := db.Exec(`INSERT INTO t (k, v) VALUES (?, ?)`, i, strings.Repeat("x", 300)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// Rewrite half the rows (exercises slot supersession + COW) and delete a quarter.
	for i := 0; i < 200; i += 2 {
		if _, err := db.Exec(`UPDATE t SET v = ? WHERE k = ?`, strings.Repeat("y", 500), i); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	if _, err := db.Exec(`DELETE FROM t WHERE k % 4 = 1`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db = openLive(t, path, Options{})
	defer db.Close()
	var n, updated int
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 150 { // 200 - 50 deleted (k % 4 == 1 → 50 rows)
		t.Fatalf("count = %d, want 150", n)
	}
	if err := db.QueryRow(`SELECT count(*) FROM t WHERE v = ?`, strings.Repeat("y", 500)).Scan(&updated); err != nil {
		t.Fatalf("count updated: %v", err)
	}
	if updated == 0 {
		t.Fatalf("expected updated rows to persist")
	}
	var ic string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check = (%q, %v), want ok", ic, err)
	}
}

func TestLiveRejectsForeignFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raw.db")

	// Create a plain (uncompressed) SQLite database.
	plain, err := sqlite.Open(sqlite.Config{Path: path})
	if err != nil {
		t.Fatalf("open plain: %v", err)
	}
	if _, err := plain.Exec(`CREATE TABLE t (x)`); err != nil {
		t.Fatalf("plain create: %v", err)
	}
	if err := plain.Close(); err != nil {
		t.Fatalf("plain close: %v", err)
	}
	before := fileSize(t, path)

	// OpenLive must refuse it rather than treat it as a container and clobber it.
	db, err := OpenLive(sqlite.Config{Path: path}, Options{})
	if err == nil {
		db.Close()
		t.Fatalf("OpenLive on a raw .db succeeded; want a no-clobber rejection")
	}
	if got := fileSize(t, path); got != before {
		t.Fatalf("raw file changed size %d → %d on a rejected OpenLive", before, got)
	}
}

func TestNewVFSRejectsBadGeometry(t *testing.T) {
	if _, err := NewVFS(Options{PageSize: 1000}); err == nil {
		t.Fatalf("non-power-of-two PageSize accepted")
	}
	if _, err := NewVFS(Options{PageSize: 4096, BlockSize: 8192}); err == nil {
		t.Fatalf("BlockSize > PageSize accepted")
	}
	// A valid geometry registers and unregisters cleanly.
	v, err := NewVFS(Options{PageSize: 8192, BlockSize: 512})
	if err != nil {
		t.Fatalf("valid geometry rejected: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("second Close not idempotent: %v", err)
	}
}
