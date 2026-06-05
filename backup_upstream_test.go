// Vendored from modernc.org/sqlite (see go.mod for the pin) to run against the fork in this
// repository. Build tag `modernc_upstream` keeps it out of the default
// `go test ./...` so the long-running upstream suite stays opt-in.
// See docs/modernc-upstream.md for the reproduction recipe.

//go:build modernc_upstream

// Copyright 2025 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0 license that can be
// found in the LICENSE file.

package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestBackupCommitClosesConnOnError verifies that Commit() closes the
// destination connection when sqlite3_backup_finish returns an error.
// Before the fix, the error branch returned (nil, err) without closing
// dstConn, leaking the file descriptor, TLS, and memory.
//
// Strategy: create a backup whose destination is a file-based database,
// then make the destination file read-only so that backup_step fails
// with an I/O or read-only error. Commit() must still close the
// destination connection.
func TestBackupCommitClosesConnOnError(t *testing.T) {
	// Create source in-memory database with some data.
	srcConn, err := newConn(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer srcConn.Close()

	// Populate source so there is at least one page to copy.
	if err := execConn(srcConn, "CREATE TABLE t(x)"); err != nil {
		t.Fatal(err)
	}
	if err := execConn(srcConn, "INSERT INTO t VALUES('hello')"); err != nil {
		t.Fatal(err)
	}

	// Create a temp directory for the destination database.
	tmpDir, err := os.MkdirTemp("", "backup_commit_test_")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dstPath := filepath.Join(tmpDir, "dst.db")

	// Create the backup object (this opens the destination connection).
	bck, err := srcConn.NewBackup(dstPath)
	if err != nil {
		t.Fatal(err)
	}

	// Make the destination directory read-only so that writes to the
	// destination database fail. This causes backup_step to return an
	// error (SQLITE_READONLY or SQLITE_IOERR), which backup_finish
	// propagates.
	if err := os.Chmod(dstPath, 0o444); err != nil {
		t.Skipf("chmod not supported: %v", err)
	}
	if err := os.Chmod(tmpDir, 0o555); err != nil {
		t.Skipf("chmod not supported: %v", err)
	}
	// Restore write permission on cleanup so RemoveAll succeeds.
	defer os.Chmod(tmpDir, 0o755)

	// Step should fail because the destination is read-only.
	_, stepErr := bck.Step(-1)

	// Capture the destination conn before Commit: on the error path Commit
	// closes the destination and clears b.dstConn (b.dstConn = nil), so we
	// keep our own handle to assert the close actually happened.
	dstBefore := bck.dstConn

	// Whether or not Step reported the error, Commit must propagate the
	// error from backup_finish and close the destination connection.
	conn, commitErr := bck.Commit()

	if stepErr == nil && commitErr == nil {
		// If neither errored, the test setup didn't achieve its goal.
		// Close the returned connection to avoid a leak in this edge case
		// and skip.
		if conn != nil {
			conn.Close()
		}
		t.Log("chmod did not restrict writes on this platform")
		t.Skip("could not provoke backup error on this platform; skipping")
	}

	if conn != nil {
		t.Fatal("Commit() must return nil conn on error")
	}

	// The critical assertion: the destination connection must be closed.
	// Commit closes it and clears b.dstConn, so check the captured handle:
	// Close() zeros db.
	if dstBefore == nil || dstBefore.db != 0 {
		t.Fatal("Commit() did not close the destination connection on error")
	}
}

// execConn is a test helper that executes a SQL statement on a raw conn.
func execConn(c *conn, sql string) error {
	s, err := c.prepare(context.Background(), sql)
	if err != nil {
		return err
	}
	defer s.Close()
	_, err = s.(*stmt).ExecContext(context.Background(), nil)
	return err
}
