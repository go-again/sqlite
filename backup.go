// Copyright 2025 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0 license that can be
// found in the LICENSE file.

package sqlite // import "github.com/go-again/sqlite"

import (
	"database/sql/driver"
	"errors"

	sqlite3 "modernc.org/sqlite/lib"
)

// Backup object is used to manage progress and cleanup an online backup. It
// is returned by NewBackup or NewRestore.
type Backup struct {
	srcConn *conn   // source database connection
	dstConn *conn   // destination database connection
	pBackup uintptr // sqlite3_backup object pointer
}

// Step will copy up to n pages between the source and destination databases
// specified by the backup object. If n is negative, all remaining source
// pages are copied.
// If it successfully copies n pages and there are still more pages to be
// copied, then the function returns true with no error. If it successfully
// finishes copying all pages from source to destination, then it returns
// false with no error. If an error occurs while running, then an error is
// returned.
func (b *Backup) Step(n int32) (bool, error) {
	rc := sqlite3.Xsqlite3_backup_step(b.srcConn.tls, b.pBackup, n)
	if rc == sqlite3.SQLITE_OK {
		return true, nil
	} else if rc == sqlite3.SQLITE_DONE {
		return false, nil
	} else {
		return false, b.srcConn.errstr(rc)
	}
}

// Finish releases all resources associated with the Backup object. The Backup
// object is invalid and may not be used following a call to Finish.
//
// When the Backup was produced by NewBackup or NewRestore, the destination
// connection it implicitly opened is also closed. When the Backup was produced
// by (*Conn).Backup (the mattn-compat factory), the destination connection is
// owned by the caller and is not closed here.
//
// Calling Finish after Close (or vice versa) is a no-op.
func (b *Backup) Finish() error {
	if b.pBackup == 0 || b.srcConn == nil {
		return nil
	}
	rc := sqlite3.Xsqlite3_backup_finish(b.srcConn.tls, b.pBackup)
	b.pBackup = 0
	if b.dstConn != nil {
		b.dstConn.Close()
		b.dstConn = nil
	}
	if rc == sqlite3.SQLITE_OK {
		return nil
	}
	return b.srcConn.errstr(rc)
}

// Commit releases all resources associated with the Backup object but does not
// close the destination database connection.
//
// The destination database connection is returned to the caller or an error if raised.
// It is the responsibility of the caller to handle the connection closure.
//
// Calling Commit followed by Finish (or Close) is safe — Commit zeros the
// internal backup handle so the follow-up is a no-op rather than a
// double sqlite3_backup_finish call on the same C handle.
func (b *Backup) Commit() (driver.Conn, error) {
	if b.pBackup == 0 || b.srcConn == nil {
		return nil, errors.New("sqlite: Commit on closed Backup")
	}
	rc := sqlite3.Xsqlite3_backup_finish(b.srcConn.tls, b.pBackup)
	b.pBackup = 0
	dst := b.dstConn
	b.dstConn = nil

	if rc == sqlite3.SQLITE_OK {
		return dst, nil
	}
	if dst != nil {
		dst.Close()
	}
	return nil, b.srcConn.errstr(rc)
}
