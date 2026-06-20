package cksm_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs/cksm"
)

// TestChain_PlainCksmWithoutCrypto confirms the single-layer cksm path
// works. (The crypto-over-cksm composition test lives in the vfs/crypto
// module, which may import cksm; cksm must not import crypto.)
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
