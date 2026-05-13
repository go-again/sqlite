// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package vfs_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs"
)

// TestVFS_OpenFromTestingFS builds a real SQLite file on disk, copies its
// bytes into a testing/fstest.MapFS, registers that FS via vfs.New, and
// confirms we can open the database in read-only mode.
func TestVFS_OpenFromTestingFS(t *testing.T) {
	// Step 1: create a real DB on disk to capture the proper SQLite byte
	// layout. Doing this once per test is cheap and avoids hard-coding the
	// SQLite header in the test source.
	tmp := filepath.Join(t.TempDir(), "seed.db")
	src, err := sql.Open("sqlite3", tmp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO t (id, name) VALUES (1, 'alpha'), (2, 'beta');`); err != nil {
		t.Fatal(err)
	}
	src.Close()

	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: serve those bytes via a testing/fstest.MapFS at "seed.db".
	mapFS := fstest.MapFS{
		"seed.db": &fstest.MapFile{Data: data},
	}
	name, _, err := vfs.New(mapFS)
	if err != nil {
		t.Fatalf("vfs.New: %v", err)
	}

	// Step 3: open the DB through the registered VFS and read back.
	dsn := "file:seed.db?vfs=" + name + "&mode=ro"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT id, name FROM t ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []struct {
		id   int
		name string
	}
	for rows.Next() {
		var r struct {
			id   int
			name string
		}
		if err := rows.Scan(&r.id, &r.name); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 || got[0].name != "alpha" || got[1].name != "beta" {
		t.Errorf("got %+v, want [{1 alpha} {2 beta}]", got)
	}
}
