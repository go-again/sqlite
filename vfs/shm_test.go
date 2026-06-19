package vfs_test

import (
	"database/sql"
	"sync"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs"
)

// walVFS is refMemVFS upgraded for WAL: its files additionally implement
// vfs.ShmFile by declaring a sharing group (the file name), which is all
// the dispatcher needs to hand them a shared WAL index. Two connections
// that open the same name share both the page store and the shm group.
type walVFS struct{ *refMemVFS }

func newWalVFS() *walVFS { return &walVFS{refMemVFS: newRefMemVFS()} }

func (v *walVFS) Open(name string, flags vfs.OpenFlags) (vfs.File, vfs.OpenFlags, error) {
	f, granted, err := v.refMemVFS.Open(name, flags)
	if err != nil {
		return nil, granted, err
	}
	return &walFile{File: f, name: name}, granted, nil
}

type walFile struct {
	vfs.File
	name string
}

// ShmGroup ties this file's WAL shared memory to sibling connections
// opening the same name.
func (f *walFile) ShmGroup() string { return f.name }

var _ vfs.ShmFile = (*walFile)(nil)

// TestUserVFS_WAL is the Phase-2 feasibility gate: a custom Go VFS that
// implements vfs.ShmFile actually enters WAL mode and round-trips a
// full DDL + DML + transaction cycle through the dispatcher-owned shared
// memory and lock table.
func TestUserVFS_WAL(t *testing.T) {
	name := uniqueVFSName("refwal")
	if err := vfs.Register(name, newWalVFS()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	db, err := sql.Open(sqlite.DriverName, "file:wal.db?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		db.Close()
		if err := vfs.Unregister(name); err != nil {
			t.Errorf("Unregister: %v", err)
		}
	})

	var jm string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&jm); err != nil {
		t.Fatalf("set WAL: %v", err)
	}
	if jm != "wal" {
		t.Fatalf("journal_mode=%q, want \"wal\" — shm/WAL did not engage", jm)
	}

	if _, err := db.Exec(`CREATE TABLE t(k INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES (1,'a'),(2,'b')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO t VALUES (3,'c')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("count=%d, want 3", n)
	}

	// A passive checkpoint exercises the exclusive WAL lock slots.
	if _, err := db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
}

// TestUserVFS_WAL_SharedReaders proves the dispatcher-owned shm group is
// genuinely shared: a second connection pool opening the same name sees
// rows a first pool committed under WAL, coordinated entirely through
// the in-process shm lock table.
func TestUserVFS_WAL_SharedReaders(t *testing.T) {
	name := uniqueVFSName("refwalshare")
	if err := vfs.Register(name, newWalVFS()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	dsn := "file:shared.db?vfs=" + name

	writer, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	reader, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	reader.SetMaxOpenConns(1)
	t.Cleanup(func() {
		writer.Close()
		reader.Close()
		if err := vfs.Unregister(name); err != nil {
			t.Errorf("Unregister: %v", err)
		}
	})

	var jm string
	if err := writer.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&jm); err != nil || jm != "wal" {
		t.Fatalf("set WAL: jm=%q err=%v", jm, err)
	}
	if _, err := writer.Exec(`CREATE TABLE t(v INT); INSERT INTO t VALUES (10),(20)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var sum int
	if err := reader.QueryRow(`SELECT coalesce(sum(v),0) FROM t`).Scan(&sum); err != nil {
		t.Fatalf("reader query: %v", err)
	}
	if sum != 30 {
		t.Fatalf("reader sum=%d, want 30 (shm not shared across connections?)", sum)
	}
}

// TestUserVFS_WAL_Concurrent exercises the shm lock table under real
// contention: one writer pool and several reader pools hammer a shared
// WAL database concurrently. WAL's invariant is that readers never block
// the writer and vice versa, so with a busy timeout for the occasional
// checkpoint contention none of them should error, and the row count
// must climb monotonically. Run under -race this is the load-bearing
// check on the lock arbitration.
func TestUserVFS_WAL_Concurrent(t *testing.T) {
	name := uniqueVFSName("refwalrace")
	if err := vfs.Register(name, newWalVFS()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	dsn := "file:race.db?vfs=" + name + "&_busy_timeout=2000"

	writer, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	if _, err := writer.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("WAL: %v", err)
	}
	if _, err := writer.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	const writes = 200
	const readers = 4
	var wg sync.WaitGroup

	wg.Go(func() {
		for range writes {
			if _, err := writer.Exec(`INSERT INTO t DEFAULT VALUES`); err != nil {
				t.Errorf("insert: %v", err)
				return
			}
		}
	})

	for id := range readers {
		rd, err := sql.Open(sqlite.DriverName, dsn)
		if err != nil {
			t.Fatal(err)
		}
		rd.SetMaxOpenConns(1)
		wg.Go(func() {
			defer rd.Close()
			last := 0
			for range writes {
				var n int
				if err := rd.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
					t.Errorf("reader %d: %v", id, err)
					return
				}
				if n < last {
					t.Errorf("reader %d saw count regress %d → %d", id, last, n)
					return
				}
				last = n
			}
		})
	}

	wg.Wait()

	// All writes must be visible after the concurrent run settles.
	var n int
	if err := writer.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("final count: %v", err)
	}
	if n != writes {
		t.Errorf("final count=%d, want %d", n, writes)
	}

	writer.Close()
	if err := vfs.Unregister(name); err != nil {
		t.Errorf("Unregister: %v", err)
	}
}
