package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	sqlite3 "modernc.org/sqlite/lib"
)

// TestInMemoryConnSurvivesInterrupt is a regression test for the ported
// modernc.org/sqlite #196 fix. An in-memory database lives only in its
// connection, so after an interrupt (e.g. a cancelled QueryContext) the
// conn must stay usable() — otherwise database/sql discards the sole
// handle and the whole store is lost. File-backed connections keep the
// #198 behaviour: an interrupted one is reported unusable so the pool
// drops it, since its data is safe on disk.
//
// Ported from modernc.org/sqlite's TestInMemoryDBSurvivesContextCancel.
func TestInMemoryConnSurvivesInterrupt(t *testing.T) {
	t.Run("in-memory stays usable after interrupt", func(t *testing.T) {
		db, err := sql.Open(driverName, "file::memory:?cache=shared")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)

		if _, err := db.Exec("CREATE TABLE t (v INT)"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO t VALUES (1), (2), (3)"); err != nil {
			t.Fatal(err)
		}

		raw, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_ = raw.Raw(func(dc any) error {
			c, ok := dc.(*conn)
			if !ok {
				t.Fatalf("driver conn is %T, want *conn", dc)
			}
			if !c.inMemory {
				t.Fatalf("conn opened with file::memory: must be marked inMemory")
			}
			if !c.usable() {
				t.Fatalf("fresh in-memory conn must be usable")
			}
			sqlite3.Xsqlite3_interrupt(c.tls, c.db)
			if !c.usable() {
				t.Errorf("in-memory conn must remain usable after interrupt (#196)")
			}
			return nil
		})
		raw.Close()

		// The store must survive: if the conn had been discarded, the
		// shared in-memory DB would be gone and the table with it.
		var n int
		if err := db.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil {
			t.Fatalf("table lost after interrupt: %v", err)
		}
		if n != 3 {
			t.Fatalf("expected 3 rows after interrupt, got %d", n)
		}
	})

	t.Run("file-backed still discarded on interrupt", func(t *testing.T) {
		dir := t.TempDir()
		db, err := sql.Open(driverName, "file:"+filepath.Join(dir, "t.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)

		raw, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		_ = raw.Raw(func(dc any) error {
			c, ok := dc.(*conn)
			if !ok {
				t.Fatalf("driver conn is %T, want *conn", dc)
			}
			if c.inMemory {
				t.Fatalf("file-backed conn unexpectedly marked inMemory")
			}
			if !c.usable() {
				t.Fatalf("fresh file-backed conn must be usable")
			}
			sqlite3.Xsqlite3_interrupt(c.tls, c.db)
			if c.usable() {
				t.Errorf("file-backed conn must be reported unusable after interrupt (#198)")
			}
			return nil
		})
	})
}
