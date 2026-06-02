// Copyright 2024 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0 license that can be
// found in the LICENSE file.

package sqlite // import "github.com/go-again/sqlite"

import (
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
