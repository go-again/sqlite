package sqlite // import "github.com/go-again/sqlite"

import (
	"errors"
	"fmt"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// Snapshot is an opaque handle to a historical state of a WAL-mode database,
// captured by [Conn.GetSnapshot]. Replay it with [Conn.OpenSnapshot] to read
// the database as it was at that point. Free it with [Snapshot.Close].
type Snapshot struct {
	c   *conn
	ptr uintptr
}

// GetSnapshot captures the current state of the schema's database as a
// Snapshot. The connection must have a read transaction open on the schema,
// and the database must be in WAL mode. Pass schema="" for "main".
//
// https://sqlite.org/c3ref/snapshot_get.html
func (c *Conn) GetSnapshot(schema string) (*Snapshot, error) {
	if schema == "" {
		schema = "main"
	}
	zDb, err := libc.CString(schema)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(c.tls, zDb)

	bp := c.tls.Alloc(int(ptrSize))
	defer c.tls.Free(int(ptrSize))
	rc := sqlite3.Xsqlite3_snapshot_get(c.tls, c.db, zDb, bp)
	if rc != sqlite3.SQLITE_OK {
		return nil, fmt.Errorf("sqlite: GetSnapshot(%q): %w", schema, c.errstr(rc))
	}
	return &Snapshot{c: c, ptr: *(*uintptr)(unsafe.Pointer(bp))}, nil
}

// OpenSnapshot starts the next read transaction on schema reading the database
// as it was when snap was captured. Call it on a connection with no open
// transaction, immediately before the BEGIN that starts the read. WAL only.
//
// https://sqlite.org/c3ref/snapshot_open.html
func (c *Conn) OpenSnapshot(schema string, snap *Snapshot) error {
	if snap == nil || snap.ptr == 0 {
		return errors.New("sqlite: OpenSnapshot: nil or closed snapshot")
	}
	if schema == "" {
		schema = "main"
	}
	zDb, err := libc.CString(schema)
	if err != nil {
		return err
	}
	defer libc.Xfree(c.tls, zDb)
	if rc := sqlite3.Xsqlite3_snapshot_open(c.tls, c.db, zDb, snap.ptr); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlite: OpenSnapshot(%q): %w", schema, c.errstr(rc))
	}
	return nil
}

// SnapshotRecover makes every historical snapshot of the schema reachable by
// [Conn.OpenSnapshot], not just states since the last checkpoint. There must
// be no read transaction open on the schema.
//
// https://sqlite.org/c3ref/snapshot_recover.html
func (c *Conn) SnapshotRecover(schema string) error {
	if schema == "" {
		schema = "main"
	}
	zDb, err := libc.CString(schema)
	if err != nil {
		return err
	}
	defer libc.Xfree(c.tls, zDb)
	if rc := sqlite3.Xsqlite3_snapshot_recover(c.tls, c.db, zDb); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlite: SnapshotRecover(%q): %w", schema, c.errstr(rc))
	}
	return nil
}

// Cmp reports whether s is older (<0), the same age (0), or newer (>0) than
// other. Both must be snapshots of the same database. A nil or closed handle
// on either side compares as 0 (equal) rather than dereferencing a NULL
// pointer in C.
//
// https://sqlite.org/c3ref/snapshot_cmp.html
func (s *Snapshot) Cmp(other *Snapshot) int {
	if s == nil || s.ptr == 0 || other == nil || other.ptr == 0 {
		return 0
	}
	return int(sqlite3.Xsqlite3_snapshot_cmp(s.c.tls, s.ptr, other.ptr))
}

// Close frees the snapshot handle. Subsequent calls are no-ops.
func (s *Snapshot) Close() {
	if s.ptr != 0 {
		sqlite3.Xsqlite3_snapshot_free(s.c.tls, s.ptr)
		s.ptr = 0
	}
}
