package vfs_test

import (
	"database/sql"
	"io"
	"io/fs"
	"sync"
	"sync/atomic"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs"
	sqlite3 "modernc.org/sqlite/lib"
)

// refMemVFS is a minimal, fully writable in-memory VFS built entirely on
// the public vfs.VFS / vfs.File interfaces. It is the dogfood proof that
// the interface is expressive enough to back a real rollback-journal
// database — and doubles as a copy-paste template for downstream
// backends. Every file (main DB and its -journal sibling) lives in one
// map keyed by name.
type refMemVFS struct {
	mu       sync.Mutex
	files    map[string]*refMemData
	readOnly bool // when true, WriteAt returns SQLITE_READONLY
}

type refMemData struct {
	mu   sync.Mutex
	data []byte

	// In-process advisory lock state, shared by every connection that opens
	// this name. NoLock is fine for a single connection, but multi-connection
	// WAL needs a real lock: SQLite gates its destructive checkpoint-on-close
	// (which resets the -wal) on acquiring an EXCLUSIVE db-file lock, so that
	// EXCLUSIVE must fail while other connections hold SHARED. The arbitration is
	// the shared vfs.AdvisoryLock helper (this is also its reference usage).
	lk vfs.AdvisoryLock
}

func newRefMemVFS() *refMemVFS { return &refMemVFS{files: map[string]*refMemData{}} }

func (v *refMemVFS) Open(name string, flags vfs.OpenFlags) (vfs.File, vfs.OpenFlags, error) {
	// An empty name is an anonymous temp file: never shared, never reopened.
	if name == "" {
		return &refMemFile{v: v, d: &refMemData{}}, flags, nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	d := v.files[name]
	if d == nil {
		if !flags.Has(vfs.OpenCreate) {
			return nil, 0, &vfs.VFSError{Code: sqlite3.SQLITE_CANTOPEN}
		}
		d = &refMemData{}
		v.files[name] = d
	}
	return &refMemFile{v: v, name: name, d: d}, flags, nil
}

func (v *refMemVFS) Delete(name string, _ bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.files[name]; !ok {
		return fs.ErrNotExist
	}
	delete(v.files, name)
	return nil
}

func (v *refMemVFS) Access(name string, _ vfs.AccessOp) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.files[name]
	return ok, nil
}

func (v *refMemVFS) FullPathname(name string) (string, error) { return name, nil }

type refMemFile struct {
	v    *refMemVFS
	name string
	d    *refMemData
	lock vfs.LockLevel // this connection's current advisory lock level
}

// Lock/Unlock/CheckReservedLock forward to the shared vfs.AdvisoryLock — the
// canonical way a multi-connection pure-Go VFS arbitrates the file-locking
// protocol (many SHARED holders, one RESERVED..EXCLUSIVE writer).
func (f *refMemFile) Lock(level vfs.LockLevel) error {
	return f.d.lk.Lock(f, &f.lock, level)
}

func (f *refMemFile) Unlock(level vfs.LockLevel) error {
	return f.d.lk.Unlock(f, &f.lock, level)
}

func (f *refMemFile) CheckReservedLock() (bool, error) {
	return f.d.lk.CheckReservedLock()
}

func (f *refMemFile) ReadAt(p []byte, off int64) (int, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if off >= int64(len(f.d.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.d.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *refMemFile) WriteAt(p []byte, off int64) (int, error) {
	if f.v.readOnly {
		return 0, &vfs.VFSError{Code: sqlite3.SQLITE_READONLY}
	}
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if end := off + int64(len(p)); end > int64(len(f.d.data)) {
		grown := make([]byte, end)
		copy(grown, f.d.data)
		f.d.data = grown
	}
	copy(f.d.data[off:], p)
	return len(p), nil
}

func (f *refMemFile) Truncate(size int64) error {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if size < int64(len(f.d.data)) {
		f.d.data = f.d.data[:size]
		return nil
	}
	grown := make([]byte, size)
	copy(grown, f.d.data)
	f.d.data = grown
	return nil
}

func (f *refMemFile) Sync(vfs.SyncFlags) error { return nil }

func (f *refMemFile) Size() (int64, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	return int64(len(f.d.data)), nil
}

func (f *refMemFile) SectorSize() int                        { return 512 }
func (f *refMemFile) DeviceCharacteristics() vfs.DeviceFlags { return 0 }
func (f *refMemFile) Close() error {
	_ = f.Unlock(vfs.LockNone)
	return nil
}

// compile-time proof the reference types satisfy the public interfaces.
var (
	_ vfs.VFS  = (*refMemVFS)(nil)
	_ vfs.File = (*refMemFile)(nil)
)

// uniqueVFSName hands out a distinct registration name per test so the
// process-global registry never collides across the suite.
var vfsNameSeq atomic.Int64

func uniqueVFSName(prefix string) string {
	return prefix + "_" + string(rune('a'+int(vfsNameSeq.Add(1)%26))) + "_test"
}

// TestUserVFS_ReadWrite is the end-to-end dogfood: a Go-implemented VFS
// backs a writable rollback-journal database through database/sql, and
// survives a full DDL + DML + transaction (commit and rollback) cycle.
func TestUserVFS_ReadWrite(t *testing.T) {
	name := uniqueVFSName("refmem")
	impl := newRefMemVFS()
	if err := vfs.Register(name, impl); err != nil {
		t.Fatalf("Register: %v", err)
	}

	db, err := sql.Open(sqlite.DriverName, "file:app.db?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	// Single connection keeps this rollback-journal smoke test simple.
	db.SetMaxOpenConns(1)

	t.Cleanup(func() {
		db.Close()
		if err := vfs.Unregister(name); err != nil {
			t.Errorf("Unregister: %v", err)
		}
	})

	// Force rollback-journal mode (Phase 1 has no WAL/shm).
	var jm string
	if err := db.QueryRow(`PRAGMA journal_mode=DELETE`).Scan(&jm); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if jm == "wal" {
		t.Fatalf("unexpected WAL mode on a Phase-1 custom VFS")
	}

	if _, err := db.Exec(`CREATE TABLE kv (k INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO kv (k, v) VALUES (1,'one'),(2,'two'),(3,'three')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// Committed transaction.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO kv (k, v) VALUES (4,'four')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Rolled-back transaction (exercises the -journal file write+delete).
	tx2, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx2.Exec(`INSERT INTO kv (k, v) VALUES (5,'five')`); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM kv`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 { // 3 seeded + 1 committed; the rolled-back row is gone
		t.Fatalf("count=%d, want 4", n)
	}
	var v string
	if err := db.QueryRow(`SELECT v FROM kv WHERE k=4`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "four" {
		t.Fatalf("v=%q, want \"four\"", v)
	}

	// The journal file should have been deleted after the committed txn.
	impl.mu.Lock()
	_, journalLeft := impl.files["app.db-journal"]
	_, mainExists := impl.files["app.db"]
	impl.mu.Unlock()
	if journalLeft {
		t.Errorf("rollback journal not cleaned up")
	}
	if !mainExists {
		t.Errorf("main db file missing from backing store")
	}
}

// TestUserVFS_Persistence proves the backing store outlives a single
// *sql.DB: a second connection pool against the same registered VFS
// sees data written by the first.
func TestUserVFS_Persistence(t *testing.T) {
	name := uniqueVFSName("refpersist")
	if err := vfs.Register(name, newRefMemVFS()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _ = vfs.Unregister(name) })

	dsn := "file:p.db?vfs=" + name
	db1, _ := sql.Open(sqlite.DriverName, dsn)
	db1.SetMaxOpenConns(1)
	if _, err := db1.Exec(`CREATE TABLE t(x); INSERT INTO t VALUES (42)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db1.Close()

	db2, _ := sql.Open(sqlite.DriverName, dsn)
	db2.SetMaxOpenConns(1)
	defer db2.Close()
	var x int
	if err := db2.QueryRow(`SELECT x FROM t`).Scan(&x); err != nil {
		t.Fatalf("reopen read: %v", err)
	}
	if x != 42 {
		t.Fatalf("x=%d, want 42", x)
	}
}

// TestUserVFS_ReadOnlyError confirms a *VFSError carrying SQLITE_READONLY
// from File.WriteAt propagates back out as a write failure.
func TestUserVFS_ReadOnlyError(t *testing.T) {
	name := uniqueVFSName("refro")
	impl := newRefMemVFS()
	if err := vfs.Register(name, impl); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _ = vfs.Unregister(name) })

	dsn := "file:ro.db?vfs=" + name
	// First build a valid database, then flip the backend read-only.
	db, _ := sql.Open(sqlite.DriverName, dsn)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE t(x)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()

	impl.readOnly = true
	db2, _ := sql.Open(sqlite.DriverName, dsn)
	db2.SetMaxOpenConns(1)
	defer db2.Close()
	_, err := db2.Exec(`INSERT INTO t VALUES (1)`)
	if err == nil {
		t.Fatalf("write to read-only backend succeeded; want error")
	}
}

func TestUserVFS_RegisterValidation(t *testing.T) {
	if err := vfs.Register("", newRefMemVFS()); err == nil {
		t.Error("empty name accepted")
	}
	if err := vfs.Register("refnilcheck", nil); err == nil {
		t.Error("nil VFS accepted")
	}
	name := uniqueVFSName("refdup")
	if err := vfs.Register(name, newRefMemVFS()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vfs.Unregister(name) })
	if err := vfs.Register(name, newRefMemVFS()); err == nil {
		t.Error("duplicate name accepted")
	}
}

func TestUserVFS_Find(t *testing.T) {
	name := uniqueVFSName("reffind")
	impl := newRefMemVFS()
	if _, ok := vfs.Find(name); ok {
		t.Fatal("Find reported an unregistered name")
	}
	if err := vfs.Register(name, impl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vfs.Unregister(name) })
	got, ok := vfs.Find(name)
	if !ok || got != vfs.VFS(impl) {
		t.Fatalf("Find=%v,%v want impl,true", got, ok)
	}
}

func TestUserVFS_UnregisterRejectsOpenFiles(t *testing.T) {
	name := uniqueVFSName("refbusy")
	if err := vfs.Register(name, newRefMemVFS()); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open(sqlite.DriverName, "file:busy.db?vfs="+name)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE t(x)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Pin one open connection so the main-db file stays open.
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := vfs.Unregister(name); err == nil {
		t.Error("Unregister succeeded while a file was open")
	}
	conn.Close()
	db.Close()
	if err := vfs.Unregister(name); err != nil {
		t.Errorf("Unregister after close: %v", err)
	}
}
