package sqlite // import "gosqlite.org"

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// Blob is a handle to a single open BLOB or TEXT value referenced by rowid,
// returned from Conn.OpenBlob. It exposes incremental binary I/O via
// sqlite3_blob_read / sqlite3_blob_write, plus zero-allocation re-binding to
// another row in the same table via Reopen.
//
// A Blob is bound to the connection that opened it and is not safe for
// concurrent use; callers must serialize access. Like the connection it is
// drawn from, the Blob must be closed explicitly via Close.
type Blob struct {
	c      *conn
	pBlob  uintptr // *sqlite3_blob
	size   int64
	offset int64
	write  bool

	mu     sync.Mutex
	closed bool
}

// OpenBlob opens a handle to a single BLOB or TEXT value identified by
// (schema, table, column, rowid). Pass schema="main" for the default database
// or "" to use SQLite's default. If write is true the handle is opened for
// read+write; otherwise it is read-only.
//
// The blob handle is valid until Close is called or until the underlying row
// changes in a way that invalidates the handle (in which case I/O operations
// return SQLITE_ABORT). Use Reopen to rebind a single handle across many
// rows in the same column without reallocating, which is materially cheaper
// than repeated OpenBlob/Close cycles.
//
// Wraps sqlite3_blob_open: https://sqlite.org/c3ref/blob_open.html
func (c *Conn) OpenBlob(schema, table, column string, rowid int64, write bool) (*Blob, error) {
	if schema == "" {
		schema = "main"
	}
	zSchema, err := libc.CString(schema)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(c.tls, zSchema)
	zTable, err := libc.CString(table)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(c.tls, zTable)
	zColumn, err := libc.CString(column)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(c.tls, zColumn)

	flag := int32(0)
	if write {
		flag = 1
	}

	// The output blob-handle slot must live in C-managed memory, not on the
	// Go stack: sqlite3_blob_open writes *ppBlob through the pointer, and a
	// Go stack address passed as uintptr is not tracked by the runtime, so a
	// stack move (likely when this runs reentrantly from inside a UDF/vtab
	// step, deep in the call graph) would leave the write targeting stale
	// memory and our handle reading as 0. Allocate the slot via c.malloc —
	// the same idiom conn.prepare uses for ppStmt.
	ppBlob, err := c.malloc(int(ptrSize))
	if err != nil {
		return nil, err
	}
	defer c.free(ppBlob)

	rc := sqlite3.Xsqlite3_blob_open(
		c.tls, c.db,
		zSchema, zTable, zColumn,
		rowid, flag,
		ppBlob,
	)
	pBlob := *(*uintptr)(unsafe.Pointer(ppBlob))
	if rc != sqlite3.SQLITE_OK {
		// Per the SQLite docs, sqlite3_blob_open may leave *ppBlob set even
		// on failure; close defensively before surfacing the error.
		if pBlob != 0 {
			sqlite3.Xsqlite3_blob_close(c.tls, pBlob)
		}
		return nil, c.errstr(rc)
	}

	n := sqlite3.Xsqlite3_blob_bytes(c.tls, pBlob)
	if n < 0 {
		// sqlite3_blob_bytes returns int32; a value > MaxInt32 (a 2 GiB
		// blob if the build raised SQLITE_MAX_LENGTH) sign-extends to a
		// negative int64 here, breaking every bounds check in
		// ReadAt/WriteAt. We refuse rather than serve a Blob whose size
		// reads as negative.
		sqlite3.Xsqlite3_blob_close(c.tls, pBlob)
		return nil, fmt.Errorf("sqlite: blob size %d overflows int32; use streaming reads", uint32(n))
	}
	return &Blob{
		c:     c,
		pBlob: pBlob,
		size:  int64(n),
		write: write,
	}, nil
}

// Close releases the underlying sqlite3_blob handle. Subsequent calls are
// no-ops and return nil.
func (b *Blob) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	rc := sqlite3.Xsqlite3_blob_close(b.c.tls, b.pBlob)
	b.pBlob = 0
	if rc != sqlite3.SQLITE_OK {
		return b.c.errstr(rc)
	}
	return nil
}

// Size reports the length in bytes of the bound BLOB or TEXT value at the
// time the handle was opened (or last reopened). The size is fixed for the
// life of the handle; growing the value requires UPDATEing the row.
func (b *Blob) Size() int64 { return b.size }

// Reopen rebinds the handle to a different rowid in the same database,
// table, and column without reallocating the underlying handle. Returns an
// error wrapping SQLITE_ABORT if the rebind fails (e.g. the new row does
// not exist); the handle is then unusable and should be closed.
//
// Wraps sqlite3_blob_reopen: https://sqlite.org/c3ref/blob_reopen.html
func (b *Blob) Reopen(rowid int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("sqlite: Blob.Reopen on closed handle")
	}
	rc := sqlite3.Xsqlite3_blob_reopen(b.c.tls, b.pBlob, rowid)
	if rc != sqlite3.SQLITE_OK {
		return b.c.errstr(rc)
	}
	n := sqlite3.Xsqlite3_blob_bytes(b.c.tls, b.pBlob)
	if n < 0 {
		// Same overflow guard as OpenBlob — refuse to serve a Blob
		// whose size is unrepresentable as a non-negative int64.
		return fmt.Errorf("sqlite: blob size %d overflows int32; use streaming reads", uint32(n))
	}
	b.size = int64(n)
	b.offset = 0
	return nil
}

// ReadAt implements io.ReaderAt. It reads up to len(p) bytes starting at
// off into p. Reads past the end of the value return io.EOF; reads that
// straddle the end return the partial bytes plus io.EOF on the next call.
//
// Wraps sqlite3_blob_read: https://sqlite.org/c3ref/blob_read.html
func (b *Blob) ReadAt(p []byte, off int64) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, errors.New("sqlite: Blob.ReadAt on closed handle")
	}
	if off < 0 {
		return 0, errors.New("sqlite: Blob.ReadAt: negative offset")
	}
	if off >= b.size {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > b.size-off {
		n = int(b.size - off)
	}
	if n == 0 {
		return 0, nil
	}
	// sqlite3_blob_read/write take int32 size + offset. BLOBs > 2 GiB
	// are pathological in SQLite (rowids are int64 but per-blob byte
	// length is int32) but error rather than silently truncate.
	if off > math.MaxInt32 || n > math.MaxInt32 {
		return 0, errors.New("sqlite: Blob.ReadAt: offset or length exceeds int32")
	}
	rc := sqlite3.Xsqlite3_blob_read(
		b.c.tls, b.pBlob,
		uintptr(unsafe.Pointer(unsafe.SliceData(p))),
		int32(n), int32(off),
	)
	if rc != sqlite3.SQLITE_OK {
		return 0, b.c.errstr(rc)
	}
	if int64(n)+off >= b.size {
		return n, io.EOF
	}
	return n, nil
}

// WriteAt implements io.WriterAt. Returns an error if the handle was
// opened read-only, or if the write would extend past the current value
// size (BLOB writes cannot grow the row — the row must be sized at INSERT
// time with zeroblob(N) or via an UPDATE).
//
// Wraps sqlite3_blob_write: https://sqlite.org/c3ref/blob_write.html
func (b *Blob) WriteAt(p []byte, off int64) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, errors.New("sqlite: Blob.WriteAt on closed handle")
	}
	if !b.write {
		return 0, errors.New("sqlite: Blob.WriteAt on read-only handle")
	}
	if off < 0 {
		return 0, errors.New("sqlite: Blob.WriteAt: negative offset")
	}
	if int64(len(p))+off > b.size {
		return 0, errors.New("sqlite: Blob.WriteAt: write past end of blob (use zeroblob(N) to pre-size)")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off > math.MaxInt32 || len(p) > math.MaxInt32 {
		return 0, errors.New("sqlite: Blob.WriteAt: offset or length exceeds int32")
	}
	rc := sqlite3.Xsqlite3_blob_write(
		b.c.tls, b.pBlob,
		uintptr(unsafe.Pointer(unsafe.SliceData(p))),
		int32(len(p)), int32(off),
	)
	if rc != sqlite3.SQLITE_OK {
		return 0, b.c.errstr(rc)
	}
	return len(p), nil
}

// Read implements io.Reader, advancing an internal cursor. The cursor
// starts at 0 and is reset by Reopen. Mix freely with ReadAt; ReadAt does
// not advance the cursor.
func (b *Blob) Read(p []byte) (int, error) {
	n, err := b.ReadAt(p, b.offset)
	b.mu.Lock()
	b.offset += int64(n)
	b.mu.Unlock()
	return n, err
}

// Write implements io.Writer, advancing the same cursor as Read.
func (b *Blob) Write(p []byte) (int, error) {
	n, err := b.WriteAt(p, b.offset)
	b.mu.Lock()
	b.offset += int64(n)
	b.mu.Unlock()
	return n, err
}

// Seek implements io.Seeker over the cursor used by Read/Write.
func (b *Blob) Seek(offset int64, whence int) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, errors.New("sqlite: Blob.Seek on closed handle")
	}
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = b.offset + offset
	case io.SeekEnd:
		abs = b.size + offset
	default:
		return 0, errors.New("sqlite: Blob.Seek: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("sqlite: Blob.Seek: negative position")
	}
	b.offset = abs
	return abs, nil
}
