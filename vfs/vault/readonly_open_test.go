package vault

import (
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
)

// TestReadOnlyOpenAfterWAL reproduces the sqlitefs bug: a read-only open of a vault
// image whose at-rest journal mode is not DELETE (e.g. left in WAL by a prior mounted
// session) must succeed. The open-setup used to run `PRAGMA journal_mode = DELETE`
// unconditionally — a header write that fails on a read-only connection with
// SQLITE_READONLY, breaking every RO headless command (ls/cat/stat/...) on a
// previously-mounted encrypted image. The read-only open must now skip that pragma.
func TestReadOnlyOpenAfterWAL(t *testing.T) {
	for _, enc := range []struct {
		name string
		opts Options // one fixed key per case, reused by both opens
	}{
		{"plaintext", Options{}},
		{"encrypted", Options{Key: randKey(t)}},
	} {
		t.Run(enc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ro.db")

			// Read-write open in WAL, write, close — the file's at-rest journal mode is
			// left as WAL (WAL mode persists in the header).
			rw, err := Open(sqlite.Config{Path: path, Pragmas: sqlite.Pragmas{JournalMode: sqlite.JournalWAL}}, enc.opts)
			if err != nil {
				t.Fatalf("rw open: %v", err)
			}
			mustExec(t, rw, `CREATE TABLE t(k INTEGER PRIMARY KEY, v TEXT)`)
			for i := range 20 {
				mustExec(t, rw, `INSERT INTO t(k, v) VALUES(?, ?)`, i, "row")
			}
			if err := rw.Close(); err != nil {
				t.Fatalf("rw close: %v", err)
			}

			// Read-only reopen: previously failed with SQLITE_READONLY on the
			// journal_mode pragma; must now open and read.
			ro, err := Open(sqlite.Config{Path: path, Mode: sqlite.ModeReadOnly}, enc.opts)
			if err != nil {
				t.Fatalf("read-only reopen: %v", err)
			}
			defer ro.Close()
			var n int
			if err := ro.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 20 {
				t.Fatalf("read-only count = (%d, %v), want 20", n, err)
			}
			// It is genuinely read-only: a write is refused (the header was never
			// rewritten to make it writable).
			if _, err := ro.Exec(`INSERT INTO t(k, v) VALUES(999, 'x')`); err == nil {
				t.Error("read-only open accepted a write")
			}
		})
	}
}
