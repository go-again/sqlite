package crypto_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs/cksm"
	"gosqlite.org/vfs/crypto"
)

// TestChain_CksmWrappedByCrypto stacks crypto on top of cksm: every write
// goes crypto → encrypts → cksm → stamps checksum trailer → disk; every read
// reverses. Both layers must engage for the data to round-trip. (This test
// lives in the crypto module because it imports cksm; cksm must not import
// crypto. The package-wide -race skip in TestMain covers it.)
func TestChain_CksmWrappedByCrypto(t *testing.T) {
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

	var n int
	if err := sc.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 30 {
		t.Errorf("count=%d, want 30", n)
	}

	// The on-disk bytes must NOT be the plaintext header — the encryption
	// layer should have transformed them.
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
