// vfs-custom: implement a SQLite VFS in pure Go on the public
// vfs.VFS / vfs.File interface, register it, and run a writable database
// against it through database/sql — no fork, no CGo, no disk.
//
// The memVFS below is a complete, ~80-line in-memory backend: a map of
// named files, each a growable byte slice. Swap the storage for an S3
// bucket, a fault injector, or a tmpfs-on-a-budget and the rest of the
// program is unchanged.
//
// Run with:
//
//	just example vfs-custom
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"log"
	"sync"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs"
)

// memVFS is a writable in-memory VFS. Every file (the main database and
// its rollback -journal sibling) lives in one name-keyed map.
type memVFS struct {
	mu    sync.Mutex
	files map[string]*memData
}

type memData struct {
	mu   sync.Mutex
	data []byte
}

func (v *memVFS) Open(name string, flags vfs.OpenFlags) (vfs.File, vfs.OpenFlags, error) {
	if name == "" { // anonymous temp file: never shared, never reopened
		return &memFile{d: &memData{}}, flags, nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	d := v.files[name]
	if d == nil {
		if !flags.Has(vfs.OpenCreate) {
			return nil, 0, vfs.Errno(14) // SQLITE_CANTOPEN
		}
		d = &memData{}
		v.files[name] = d
	}
	return &memFile{d: d}, flags, nil
}

func (v *memVFS) Delete(name string, _ bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.files[name]; !ok {
		return fs.ErrNotExist // → SQLITE_IOERR_DELETE_NOENT
	}
	delete(v.files, name)
	return nil
}

func (v *memVFS) Access(name string, _ vfs.AccessOp) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.files[name]
	return ok, nil
}

// FullPathname is the cache key that ties a database to its journal.
// A flat in-memory namespace returns the name unchanged.
func (v *memVFS) FullPathname(name string) (string, error) { return name, nil }

// memFile embeds vfs.NoLock — a single-process backend accepts every
// advisory lock, so the lock trio needs no real implementation.
type memFile struct {
	vfs.NoLock
	d *memData
}

func (f *memFile) ReadAt(p []byte, off int64) (int, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	if off >= int64(len(f.d.data)) {
		return 0, io.EOF // dispatcher zero-fills + reports SHORT_READ
	}
	n := copy(p, f.d.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *memFile) WriteAt(p []byte, off int64) (int, error) {
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

func (f *memFile) Truncate(size int64) error {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	grown := make([]byte, size)
	copy(grown, f.d.data)
	f.d.data = grown
	return nil
}

func (f *memFile) Size() (int64, error) {
	f.d.mu.Lock()
	defer f.d.mu.Unlock()
	return int64(len(f.d.data)), nil
}

func (f *memFile) Sync(vfs.SyncFlags) error               { return nil } // nothing to flush
func (f *memFile) SectorSize() int                        { return 512 }
func (f *memFile) DeviceCharacteristics() vfs.DeviceFlags { return 0 }
func (f *memFile) Close() error                           { return nil }

func main() {
	// Register the VFS once, under a name DSNs reference via ?vfs=.
	if err := vfs.Register("memexample", &memVFS{files: map[string]*memData{}}); err != nil {
		log.Fatalf("Register: %v", err)
	}
	// Unregister only after every database is closed — it refuses while
	// files are still open.
	defer func() {
		if err := vfs.Unregister("memexample"); err != nil {
			log.Printf("Unregister: %v", err)
		}
	}()

	db, err := sql.Open("sqlite", "file:app.db?vfs=memexample")
	if err != nil {
		log.Fatal(err)
	}
	// One connection: NoLock provides no cross-connection arbitration.
	db.SetMaxOpenConns(1)
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		log.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO notes(body) VALUES ('hello'),('from'),('pure-Go VFS')`); err != nil {
		log.Fatal(err)
	}

	// A real transaction: the commit writes app.db-journal through the
	// VFS, then deletes it — all in memory.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notes SET body = upper(body) WHERE id = 1`); err != nil {
		log.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}

	rows, err := db.QueryContext(ctx, `SELECT id, body FROM notes ORDER BY id`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	fmt.Println("rows read back through the custom VFS:")
	for rows.Next() {
		var id int
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %d: %s\n", id, body)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
}
