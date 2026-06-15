// Copyright 2024 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0 license that can be
// found in the LICENSE file.

package sqlite // import "github.com/go-again/sqlite"

import (
	"fmt"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// Access to sqlite3_file_control
type FileControl interface {
	// Set or query SQLITE_FCNTL_PERSIST_WAL, returns set mode or query result
	FileControlPersistWAL(dbName string, mode int) (int, error)
	// Query SQLITE_FCNTL_DATA_VERSION, returns the pager-cache data version
	// for dbName. The value changes whenever the contents of the database
	// file change, which makes it suitable for cache-invalidation use cases.
	// See
	// https://www.sqlite.org/c3ref/c_fcntl_begin_atomic_write.html#sqlitefcntldataversion.
	FileControlDataVersion(dbName string) (uint32, error)
	// Query (n<0) or set (n>=0) SQLITE_FCNTL_RESERVE_BYTES for dbName.
	// Returns the resulting reserved-bytes count. Reserved bytes are an
	// unused area at the end of each page available to extensions like
	// the checksum VFS (see github.com/go-again/sqlite/vfs/cksm).
	// Setting the value on a non-empty database does not retroactively
	// rewrite existing pages — VACUUM the database after setting it so
	// that every page is rebuilt with the new trailer width.
	FileControlReserveBytes(dbName string, n int) (int, error)
}

var _ FileControl = (*conn)(nil)

func (c *conn) FileControlPersistWAL(dbName string, mode int) (int, error) {
	pi32 := c.tls.Alloc(4)
	defer c.tls.Free(4)

	*(*int32)(unsafe.Pointer(pi32)) = int32(mode)
	err := c.fileControl(dbName, sqlite3.SQLITE_FCNTL_PERSIST_WAL, pi32)
	return int(*(*int32)(unsafe.Pointer(pi32))), err
}

func (c *conn) FileControlDataVersion(dbName string) (uint32, error) {
	pu32 := c.tls.Alloc(4)
	defer c.tls.Free(4)

	*(*uint32)(unsafe.Pointer(pu32)) = 0
	err := c.fileControl(dbName, sqlite3.SQLITE_FCNTL_DATA_VERSION, pu32)
	return *(*uint32)(unsafe.Pointer(pu32)), err
}

func (c *conn) FileControlReserveBytes(dbName string, n int) (int, error) {
	pi32 := c.tls.Alloc(4)
	defer c.tls.Free(4)

	*(*int32)(unsafe.Pointer(pi32)) = int32(n)
	err := c.fileControl(dbName, sqlite3.SQLITE_FCNTL_RESERVE_BYTES, pi32)
	return int(*(*int32)(unsafe.Pointer(pi32))), err
}

// EnableChecksums sets reserved_bytes=8 on the named schema ("main"
// for the primary database) and VACUUMs so every existing page is
// rewritten with the 8-byte trailer in place. After the call the
// header's reserved_bytes byte reads 8 and any cksm-flavored VFS
// wrapping the connection (typically [github.com/go-again/sqlite/vfs/cksm])
// will start computing + verifying page checksums.
//
// Idempotent — if reserved_bytes is already 8 the call is a no-op.
// Should be used once after opening a fresh database; on subsequent
// opens the VFS auto-detects the on-disk format and EnableChecksums
// does not need to be called again.
//
// Mirrors the same-named convenience on ncruces/go-sqlite3's *Conn.
func (c *Conn) EnableChecksums(schema string) error {
	if schema == "" {
		schema = "main"
	}
	r, err := c.FileControlReserveBytes(schema, -1)
	if err != nil {
		return fmt.Errorf("sqlite: EnableChecksums: query reserved_bytes: %w", err)
	}
	if r == 8 {
		return nil
	}
	if _, err := c.FileControlReserveBytes(schema, 8); err != nil {
		return fmt.Errorf("sqlite: EnableChecksums: set reserved_bytes: %w", err)
	}
	q := "VACUUM"
	if schema != "main" {
		q = `VACUUM "` + escapeIdent(schema) + `"`
	}
	if _, err := c.Exec(q, nil); err != nil {
		return fmt.Errorf("sqlite: EnableChecksums: VACUUM after reserved_bytes change: %w", err)
	}
	return nil
}

func escapeIdent(s string) string {
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		if s[i] == '"' {
			out = append(out, '"', '"')
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}

func (c *conn) fileControl(dbName string, op int, pArg uintptr) error {
	zDbName, err := libc.CString(dbName)
	if err != nil {
		return err
	}
	defer c.free(zDbName)

	if rc := sqlite3.Xsqlite3_file_control(c.tls, c.db, zDbName, int32(op), pArg); rc != sqlite3.SQLITE_OK {
		return c.errstr(rc)
	}

	return nil
}

// SetFileControlInt issues a file-control op whose argument is a single int,
// returning the value SQLite leaves in the slot (many such ops are
// read-modify-write — e.g. SQLITE_FCNTL_CHUNK_SIZE, SQLITE_FCNTL_PERSIST_WAL,
// SQLITE_FCNTL_POWERSAFE_OVERWRITE). schema="" means "main". It is the generic
// escape hatch over sqlite3_file_control for the int-argument ops this package
// does not wrap individually.
//
// https://sqlite.org/c3ref/file_control.html
func (c *Conn) SetFileControlInt(schema string, op, val int) (int, error) {
	if schema == "" {
		schema = "main"
	}
	p, err := c.malloc(4)
	if err != nil {
		return 0, err
	}
	defer libc.Xfree(c.tls, p)
	*(*int32)(unsafe.Pointer(p)) = int32(val)
	if err := c.fileControl(schema, op, p); err != nil {
		return 0, err
	}
	return int(*(*int32)(unsafe.Pointer(p))), nil
}

// ResetCache discards this connection's page cache for schema
// (SQLITE_FCNTL_RESET_CACHE), forcing subsequent reads to re-fetch from the VFS.
// Use it after a lower layer (a wrapping VFS, an external process) has changed
// the database file out from under SQLite. schema="" means "main".
//
// https://sqlite.org/c3ref/c_fcntl_begin_atomic_write.html
func (c *Conn) ResetCache(schema string) error {
	if schema == "" {
		schema = "main"
	}
	return c.fileControl(schema, sqlite3.SQLITE_FCNTL_RESET_CACHE, 0)
}
