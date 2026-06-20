// Package blobio exposes SQLite's incremental BLOB I/O API as SQL scalar
// functions, so callers can stream parts of large BLOBs from SQL without
// materializing the whole row into memory.
//
// # Functions
//
//   - readblob(schema, table, column, rowid, offset, n) → BLOB
//     Reads n bytes of the value at (schema, table, column, rowid) starting
//     at offset. Returns the bytes as a BLOB. Reading past the value end
//     errors.
//
//   - writeblob(schema, table, column, rowid, offset, data) → INTEGER
//     Writes data into the value at (schema, table, column, rowid) starting
//     at offset. Returns the number of bytes written. The target value must
//     be sized to hold the write (use INSERT … VALUES (zeroblob(N)) to
//     pre-size); writeblob does not grow it. Writes past the value end error.
//     Do NOT try to grow with `col || zeroblob(delta)` — SQLite drops
//     zeroblob operands under `||`, silently truncating. For an unbounded,
//     growable byte stream, use [gosqlite.org/blobstore].
//
//   - openblob(schema, table, column, rowid, write, callback, args...) → INTEGER
//     Opens a handle at (schema, table, column, rowid) and invokes callback
//     with it plus any trailing args. The callback must be bound via
//     [sqlite.Pointer] as an [OpenCallback]; the handle is valid only for the
//     duration of the call.
//
// readblob and writeblob open a fresh [gosqlite.org.Blob] handle
// on every invocation; for hot loops, prefer calling
// [gosqlite.org.Conn.OpenBlob] from Go and holding the handle
// across reads/writes.
//
// Ported from [ncruces/ext/blobio].
//
// [ncruces/ext/blobio]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/blobio
package blobio

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"

	sqlite "gosqlite.org"
)

// OpenCallback is the type for the openblob() callback. Bound via
// [sqlite.Pointer], it receives the open [sqlite.Blob] handle plus any
// trailing args from the SQL invocation. The handle is only valid for
// the duration of the callback — callers cannot retain it.
type OpenCallback func(*sqlite.Blob, ...any) error

// Register installs the readblob, writeblob, and openblob scalar
// functions on c.
//
// Registration is per-connection. For pool-wide install via
// [sqlite.Driver.ConnectHook], blank-import the auto sub-package:
//
//	import _ "gosqlite.org/ext/blobio/auto"
func Register(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc(FuncReadBlob, makeReadblob(c), false),
		c.RegisterFunc(FuncWriteBlob, makeWriteblob(c), false),
		c.RegisterFunc(FuncOpenBlob, makeOpenblob(c), false),
	)
}

// Exported names of the SQL functions Register installs.
const (
	FuncReadBlob  = "readblob"
	FuncWriteBlob = "writeblob"
	FuncOpenBlob  = "openblob"
)

func makeReadblob(c *sqlite.Conn) func(string, string, string, int64, int64, int64) ([]byte, error) {
	return func(schema, table, column string, rowid, offset, n int64) ([]byte, error) {
		if n < 0 {
			return nil, fmt.Errorf("readblob: negative byte count")
		}
		b, err := c.OpenBlob(schema, table, column, rowid, false)
		if err != nil {
			return nil, fmt.Errorf("readblob: %w", err)
		}
		defer func() { _ = b.Close() }()
		// Reject overlarge reads BEFORE allocating: an arbitrary SQL
		// caller passing `SELECT readblob(..., 0, 1e12)` would
		// otherwise allocate 1 TiB before io.ReadFull's bounds check
		// fires. Clamp to the BLOB's tail length.
		if offset < 0 {
			return nil, fmt.Errorf("readblob: negative offset %d", offset)
		}
		blobSize := b.Size()
		if offset > blobSize {
			return nil, fmt.Errorf("readblob: offset %d past blob size %d", offset, blobSize)
		}
		if n > blobSize-offset {
			return nil, fmt.Errorf("readblob: requested %d bytes but only %d available from offset %d",
				n, blobSize-offset, offset)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(io.NewSectionReader(b, offset, n), buf); err != nil {
			return nil, fmt.Errorf("readblob: %w", err)
		}
		return buf, nil
	}
}

// makeOpenblob returns the variadic openblob handler. SQL signature:
//
//	openblob(schema, table, column, rowid, write, callback, args...) → INTEGER
//
// The callback must be wrapped via sqlite.Pointer at bind time;
// trailing args (variadic, any type the driver carries) are passed
// through to the callback unchanged. Returns 0 on success, surfaces
// any error from the callback via the SQL error path.
func makeOpenblob(c *sqlite.Conn) func(args ...driver.Value) (int64, error) {
	return func(args ...driver.Value) (int64, error) {
		if len(args) < 6 {
			return 0, fmt.Errorf("openblob: expected at least 6 arguments (schema, table, column, rowid, write, callback), got %d", len(args))
		}
		schema, ok := args[0].(string)
		if !ok {
			return 0, fmt.Errorf("openblob: schema must be TEXT, got %T", args[0])
		}
		table, ok := args[1].(string)
		if !ok {
			return 0, fmt.Errorf("openblob: table must be TEXT, got %T", args[1])
		}
		column, ok := args[2].(string)
		if !ok {
			return 0, fmt.Errorf("openblob: column must be TEXT, got %T", args[2])
		}
		rowid, ok := args[3].(int64)
		if !ok {
			return 0, fmt.Errorf("openblob: rowid must be INTEGER, got %T", args[3])
		}
		write := false
		switch v := args[4].(type) {
		case int64:
			// Restrict to {0, 1}: arbitrary truthy integers (e.g. -1
			// from a typo) silently meant "write" before. Forcing the
			// caller to pick 0/1 surfaces typos.
			switch v {
			case 0:
				write = false
			case 1:
				write = true
			default:
				return 0, fmt.Errorf("openblob: write must be 0 or 1, got %d", v)
			}
		case bool:
			write = v
		default:
			return 0, fmt.Errorf("openblob: write must be INTEGER or BOOLEAN, got %T", v)
		}
		// The callback must have been bound via sqlite.Pointer at the
		// call site; if it survived the SQL round-trip as a Go value,
		// type-assert to OpenCallback.
		cb, ok := args[5].(OpenCallback)
		if !ok {
			return 0, fmt.Errorf("openblob: callback must be bound via sqlite.Pointer(OpenCallback), got %T", args[5])
		}
		trailing := make([]any, len(args)-6)
		for i, v := range args[6:] {
			trailing[i] = v
		}
		b, err := c.OpenBlob(schema, table, column, rowid, write)
		if err != nil {
			return 0, fmt.Errorf("openblob: %w", err)
		}
		defer func() { _ = b.Close() }()
		if err := cb(b, trailing...); err != nil {
			return 0, fmt.Errorf("openblob callback: %w", err)
		}
		return 0, nil
	}
}

func makeWriteblob(c *sqlite.Conn) func(string, string, string, int64, int64, driver.Value) (int64, error) {
	return func(schema, table, column string, rowid, offset int64, data driver.Value) (int64, error) {
		var p []byte
		switch v := data.(type) {
		case []byte:
			p = v
		case string:
			p = []byte(v)
		case nil:
			return 0, nil
		default:
			return 0, fmt.Errorf("writeblob: data must be BLOB or TEXT, got %T", data)
		}
		b, err := c.OpenBlob(schema, table, column, rowid, true)
		if err != nil {
			return 0, fmt.Errorf("writeblob: %w", err)
		}
		defer func() { _ = b.Close() }()
		n, err := b.WriteAt(p, offset)
		if err != nil {
			return int64(n), fmt.Errorf("writeblob: %w", err)
		}
		return int64(n), nil
	}
}
