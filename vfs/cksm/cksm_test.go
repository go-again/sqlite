package cksm_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs/cksm"
)

// openDB opens a sql.DB through a fresh cksm VFS, pins one conn, calls
// EnableChecksums to set reserved_bytes=8, and returns the conn ready
// for tests. The pin is required so we can reach the *sqlite.Conn
// behind the *sql.DB for the file-control call.
func openDB(t *testing.T, dir string) (db *sql.DB, sc *sql.Conn, name string) {
	t.Helper()
	name, fs, err := cksm.New(cksm.Options{})
	if err != nil {
		t.Fatalf("cksm.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	path := filepath.Join(dir, "cksm.db")
	db, err = sql.Open("sqlite", path+"?vfs="+name)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	sc, err = db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })

	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return cksm.EnableChecksums(c, "main")
	}); err != nil {
		t.Fatalf("EnableChecksums: %v", err)
	}

	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	return db, sc, name
}

func TestCksm_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	_, sc, _ := openDB(t, dir)
	for i := range 50 {
		if _, err := sc.ExecContext(context.Background(),
			`INSERT INTO t(v) VALUES (?)`, "row"+string(rune('a'+i%26))); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := sc.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 50 {
		t.Errorf("count=%d, want 50", count)
	}
}

func TestCksm_HeaderHasReservedBytes(t *testing.T) {
	dir := t.TempDir()
	_, sc, _ := openDB(t, dir)
	for i := range 20 {
		if _, err := sc.ExecContext(context.Background(),
			`INSERT INTO t(v) VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	// Force a sync so the header is on disk.
	if _, err := sc.ExecContext(context.Background(), `PRAGMA wal_checkpoint`); err != nil {
		t.Fatal(err)
	}
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}

	hdr := make([]byte, 100)
	f, err := os.Open(filepath.Join(dir, "cksm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Read(hdr); err != nil {
		t.Fatal(err)
	}
	if hdr[20] != 8 {
		t.Errorf("header[20] = %d, want 8 (reserved_bytes after EnableChecksums)", hdr[20])
	}
}

func TestCksm_CorruptionDetected(t *testing.T) {
	dir := t.TempDir()
	_, sc, name := openDB(t, dir)
	for i := range 20 {
		if _, err := sc.ExecContext(context.Background(),
			`INSERT INTO t(v) VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}

	// Tamper with a non-header byte inside a data page.
	path := filepath.Join(dir, "cksm.db")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 2048); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	// Reopen through the same cksm VFS — fresh page cache.
	db2, err := sql.Open("sqlite", path+"?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	_, err = db2.Query(`SELECT * FROM t`)
	if err == nil {
		t.Error("corrupted DB: want error from query, got nil")
	}
}

func TestCksm_BadPageSizeRejected(t *testing.T) {
	for _, bad := range []int{300, 5000, 100, 65537, 1024 + 1} {
		_, _, err := cksm.New(cksm.Options{PageSize: bad})
		if err == nil {
			t.Errorf("New(PageSize=%d): want error, got nil", bad)
			continue
		}
		if !strings.Contains(err.Error(), "PageSize") {
			t.Errorf("New(PageSize=%d): error=%v, want mention of PageSize", bad, err)
		}
	}
}

func TestCksm_ReopenAutoEnables(t *testing.T) {
	// After EnableChecksums + VACUUM, the header byte 20 == 8 is on
	// disk. On reopen we should auto-detect that and verify trailers
	// without the caller calling EnableChecksums a second time.
	dir := t.TempDir()
	_, sc, name := openDB(t, dir)
	for i := range 30 {
		if _, err := sc.ExecContext(context.Background(),
			`INSERT INTO t(v) VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	sc.Close()

	// Reopen — no EnableChecksums call.
	path := filepath.Join(dir, "cksm.db")
	db2, err := sql.Open("sqlite", path+"?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	var count int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("read after reopen: %v", err)
	}
	if count != 30 {
		t.Errorf("count=%d, want 30", count)
	}
}

func TestCksm_WALMode(t *testing.T) {
	dir := t.TempDir()
	_, sc, _ := openDB(t, dir)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		if _, err := sc.ExecContext(ctx, `INSERT INTO t(v) VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := sc.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 100 {
		t.Errorf("count=%d, want 100", count)
	}
}

func TestCksm_Close_Idempotent(t *testing.T) {
	_, fs, err := cksm.New(cksm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
