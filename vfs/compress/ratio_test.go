package compress

// Increment-4 ratio + throughput: the at-rest payoff (compressed container vs a
// raw database at the same page size) and the write-throughput cost of the
// compression + page-translation layer.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	sqlite "gosqlite.org"
)

// ratioPayload is a compressible, log/JSON-shaped row body.
func ratioPayload(i int) string {
	return fmt.Sprintf(`{"id":%d,"type":"event","level":"info","service":"api","msg":"%s"}`,
		i, strings.Repeat("request handled ok; ", 6))
}

// insertRows creates the table and inserts n rows in one transaction.
func insertRows(tb testing.TB, db *sqlite.DB, n int) {
	tb.Helper()
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		tb.Fatalf("create: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		tb.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO t (v) VALUES (?)`)
	if err != nil {
		tb.Fatalf("prepare: %v", err)
	}
	for i := range n {
		if _, err := stmt.Exec(ratioPayload(i)); err != nil {
			tb.Fatalf("insert %d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit: %v", err)
	}
}

// openRaw opens a plain (uncompressed) database at the live VFS's page size, so
// the size comparison is apples-to-apples.
func openRaw(tb testing.TB, path string) *sqlite.DB {
	tb.Helper()
	db, err := sqlite.Open(sqlite.Config{
		Path:         path,
		MaxOpenConns: 1,
		Pragmas:      sqlite.Pragmas{JournalMode: sqlite.JournalDelete, Extra: map[string]string{"page_size": "65536"}},
	})
	if err != nil {
		tb.Fatalf("open raw: %v", err)
	}
	return db
}

func TestLiveCompressionRatioVsRaw(t *testing.T) {
	dir := t.TempDir()
	const rows = 4000

	rawPath := filepath.Join(dir, "raw.db")
	raw := openRaw(t, rawPath)
	insertRows(t, raw, rows)
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	rawSize := fileSize(t, rawPath)

	livePath := filepath.Join(dir, "live.dbz")
	live := openLive(t, livePath, Options{Level: CompressionBetter})
	insertRows(t, live, rows)
	if err := live.Close(); err != nil {
		t.Fatalf("close live: %v", err)
	}
	liveSize := fileSize(t, livePath)

	t.Logf("rows=%d raw=%d bytes live=%d bytes (%.1f%% of raw)",
		rows, rawSize, liveSize, 100*float64(liveSize)/float64(rawSize))
	if liveSize >= rawSize {
		t.Fatalf("live compressed file (%d) is not smaller than raw (%d)", liveSize, rawSize)
	}
}

func benchInsert(b *testing.B, db *sqlite.DB) {
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		b.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO t (v) VALUES (?)`)
	if err != nil {
		b.Fatal(err)
	}
	// b.Loop manages the timer: the setup above and the commit below are excluded.
	for i := 0; b.Loop(); i++ {
		if _, err := stmt.Exec(ratioPayload(i)); err != nil {
			b.Fatal(err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkLiveInsert(b *testing.B) {
	db, err := OpenLive(sqlite.Config{Path: filepath.Join(b.TempDir(), "bench.dbz")}, Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	benchInsert(b, db)
}

func BenchmarkRawInsert(b *testing.B) {
	db := openRaw(b, filepath.Join(b.TempDir(), "bench.db"))
	defer db.Close()
	benchInsert(b, db)
}
