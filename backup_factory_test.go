// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// TestBackup_LiveDBToFile copies an in-memory database into a freshly created
// on-disk file and verifies the rows survive after reopening the file.
func TestBackup_LiveDBToFile(t *testing.T) {
	ctx := context.Background()

	// Populate a source in-memory database.
	srcDB, srcSC, srcConn := withMattnConn(t, ":memory:")
	if _, err := srcSC.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO t (id, name) VALUES (1, 'alpha'), (2, 'beta'), (3, 'gamma');`); err != nil {
		t.Fatal(err)
	}
	_ = srcDB

	// Open a destination on-disk database.
	dstPath := filepath.Join(t.TempDir(), "backup.db")
	dstDB, err := sql.Open(DriverNameMattn, dstPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dstDB.Close() })

	dstSC, err := dstDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dstSC.Close() })

	var dstConn *Conn
	if err := dstSC.Raw(func(dc any) error {
		dstConn = dc.(*Conn)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	bk, err := dstConn.Backup("main", srcConn, "main")
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Step until the source is fully copied.
	totalSteps := 0
	for {
		more, err := bk.Step(2)
		if err != nil {
			t.Fatalf("Step: %v", err)
		}
		totalSteps++
		if !more {
			break
		}
		if totalSteps > 1000 {
			t.Fatal("backup did not converge after 1000 steps")
		}
	}
	if remaining := bk.Remaining(); remaining != 0 {
		t.Errorf("Remaining()=%d after Step returned done, want 0", remaining)
	}
	if pages := bk.PageCount(); pages == 0 {
		t.Errorf("PageCount() = 0 after copy, want > 0")
	}
	if err := bk.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// Verify the destination contains the source rows.
	rows, err := dstSC.QueryContext(ctx, "SELECT id, name FROM t ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type entry struct {
		id   int
		name string
	}
	var got []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.name); err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	want := []entry{{1, "alpha"}, {2, "beta"}, {3, "gamma"}}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] got %+v, want %+v", i, got[i], w)
		}
	}
}

// TestBackup_NilSourceConn ensures Backup rejects a nil source rather than
// segfaulting.
func TestBackup_NilSourceConn(t *testing.T) {
	_, _, c := withMattnConn(t, ":memory:")
	_, err := c.Backup("main", nil, "main")
	if err == nil {
		t.Fatal("expected error from nil src conn")
	}
}

// TestSerializeDeserialize_RoundTrip dumps an in-memory database to bytes and
// reloads it into a fresh in-memory database, checking row preservation.
func TestSerializeDeserialize_RoundTrip(t *testing.T) {
	ctx := context.Background()

	src, err := sql.Open(DriverNameMattn, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	src.SetMaxOpenConns(1)

	if _, err := src.ExecContext(ctx, `CREATE TABLE m (k INTEGER PRIMARY KEY, v TEXT);
INSERT INTO m (k, v) VALUES (1, 'one'), (2, 'two');`); err != nil {
		t.Fatal(err)
	}

	data, err := Serialize(ctx, src)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Serialize returned empty bytes")
	}

	dst, err := sql.Open(DriverNameMattn, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dst.Close() })
	dst.SetMaxOpenConns(1)
	// Force the destination's only conn to be the one we Deserialize into.
	dstConn, err := dst.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer dstConn.Close()
	if err := dstConn.Raw(func(dc any) error {
		return dc.(*Conn).Deserialize(data)
	}); err != nil {
		t.Fatalf("Deserialize: %v", err)
	}

	var k int
	var v string
	if err := dstConn.QueryRowContext(ctx, "SELECT k, v FROM m ORDER BY k LIMIT 1").Scan(&k, &v); err != nil {
		t.Fatal(err)
	}
	if k != 1 || v != "one" {
		t.Errorf("got k=%d v=%q, want k=1 v=\"one\"", k, v)
	}
}

// TestLoadExtension_Disabled returns a descriptive error when EnableLoadExtension
// hasn't been called, since SQLite refuses load_extension by default.
//
// Skipped under -race: modernc's _sqlite3LoadExtension does pointer arithmetic
// that trips Go's checkptr analyzer (enabled by -race), aborting the test
// binary with "fatal error: checkptr: pointer arithmetic result points to
// invalid allocation". The underlying C code is correct; this is a known
// modernc / Go-checkptr interaction.
func TestLoadExtension_Disabled(t *testing.T) {
	if raceEnabled {
		t.Skip("modernc.org/sqlite's _sqlite3LoadExtension trips Go's checkptr under -race")
	}
	_, _, c := withMattnConn(t, ":memory:")
	err := c.LoadExtension("/nonexistent/path/foo", "")
	if err == nil {
		t.Fatal("expected error loading a disabled/missing extension")
	}
	// We don't assert the exact text — different platforms phrase it
	// differently — but we ensure an *Error came back.
	var se *Error
	if !errors.As(err, &se) {
		t.Errorf("error not *sqlite.Error: %T (%v)", err, err)
	}
}

// TestLoadExtension_EnabledBadPath enables loading and confirms a bad path
// surfaces as an error rather than crashing.
//
// Skipped on platforms where modernc.org/libc's dlopen shim is incomplete
// (e.g. darwin currently prints "Xdlopen: TODOTODO" and aborts). Also
// skipped under -race for the same checkptr reason TestLoadExtension_Disabled
// is skipped.
func TestLoadExtension_EnabledBadPath(t *testing.T) {
	if isDarwin {
		t.Skip("modernc.org/libc Xdlopen unimplemented on darwin")
	}
	if raceEnabled {
		t.Skip("modernc.org/sqlite's _sqlite3LoadExtension trips Go's checkptr under -race")
	}
	_, _, c := withMattnConn(t, ":memory:")
	if err := c.EnableLoadExtension(true); err != nil {
		t.Fatal(err)
	}
	err := c.LoadExtension("/definitely/not/an/extension", "")
	if err == nil {
		t.Fatal("expected error from missing path")
	}
}
