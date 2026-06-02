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
//     pre-size). Writes past the value end error.
//
// Both functions open a fresh [github.com/go-again/sqlite.Blob] handle on
// every invocation; for hot loops, prefer calling [github.com/go-again/sqlite.Conn.OpenBlob]
// from Go and holding the handle across reads/writes.
//
// Ported from [ncruces/ext/blobio]. The openblob() callback form is
// intentionally omitted — it relies on Go-side closures passed via
// [sqlite.Pointer], which we may revisit if consumer demand appears.
//
// [ncruces/ext/blobio]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/blobio
package blobio

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"

	sqlite "github.com/go-again/sqlite"
)

// Register installs the readblob and writeblob scalar functions on c.
//
// Registration is per-connection. For pool-wide install via
// [sqlite.Driver.ConnectHook], blank-import the auto sub-package:
//
//	import _ "github.com/go-again/sqlite/ext/blobio/auto"
func Register(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc("readblob", makeReadblob(c), false),
		c.RegisterFunc("writeblob", makeWriteblob(c), false),
	)
}

func makeReadblob(c *sqlite.Conn) func(string, string, string, int64, int64, int64) ([]byte, error) {
	return func(schema, table, column string, rowid, offset, n int64) ([]byte, error) {
		if n < 0 {
			return nil, fmt.Errorf("readblob: negative byte count")
		}
		b, err := c.OpenBlob(schema, table, column, rowid, false)
		if err != nil {
			return nil, fmt.Errorf("readblob: %w", err)
		}
		defer b.Close()
		buf := make([]byte, n)
		if _, err := io.ReadFull(io.NewSectionReader(b, offset, n), buf); err != nil {
			return nil, fmt.Errorf("readblob: %w", err)
		}
		return buf, nil
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
		defer b.Close()
		n, err := b.WriteAt(p, offset)
		if err != nil {
			return int64(n), fmt.Errorf("writeblob: %w", err)
		}
		return int64(n), nil
	}
}
