package cksm_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs/cksm"
	"github.com/go-again/sqlite/vfs/crypto"
)

// TestChain_CksmWrappedByCrypto stacks crypto on top of cksm: every
// write goes crypto → encrypts → cksm → stamps checksum trailer →
// disk; every read reverses. Both layers must engage for the data to
// round-trip.
func TestChain_CksmWrappedByCrypto(t *testing.T) {
	if raceEnabled {
		t.Skip("skipping under -race: see openDB skip note in cksm_test.go")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "chained.db")

	cksmName, cksmFS, err := cksm.New(cksm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer cksmFS.Close()

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	cryptoName, cryptoFS, err := crypto.New(crypto.Options{
		Key:     key,
		WrapVFS: cksmName,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cryptoFS.Close()

	db, err := sql.Open(sqlite.DriverName, path+"?vfs="+cryptoName)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	// Enable checksums via the cksm helper (sets reserved_bytes + VACUUM).
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return c.EnableChecksums("main")
	}); err != nil {
		t.Fatalf("EnableChecksums: %v", err)
	}

	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		if _, err := sc.ExecContext(ctx, `INSERT INTO t(v) VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}

	// Sanity: read back through the chain.
	var n int
	if err := sc.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 30 {
		t.Errorf("count=%d, want 30", n)
	}

	// The on-disk bytes must NOT be the plaintext schema — the
	// encryption layer should have transformed them. Read the raw
	// header bytes and confirm they don't start with "SQLite format 3".
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hdr := make([]byte, 16)
	if _, err := f.Read(hdr); err != nil {
		t.Fatal(err)
	}
	if string(hdr) == "SQLite format 3\x00" {
		t.Error("on-disk header is plaintext; encryption layer did not engage in the chain")
	}
}

// TestChain_PlainCksmWithoutCrypto confirms the existing single-layer
// path still works after the refactor.
func TestChain_PlainCksmWithoutCrypto(t *testing.T) {
	if raceEnabled {
		t.Skip("skipping under -race: see openDB skip note in cksm_test.go")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "plain.db")

	name, fs, err := cksm.New(cksm.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	db, err := sql.Open(sqlite.DriverName, path+"?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return c.EnableChecksums("main")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE t(v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(context.Background(),
		`INSERT INTO t VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	var got int
	if err := sc.QueryRowContext(context.Background(),
		`SELECT v FROM t`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}
