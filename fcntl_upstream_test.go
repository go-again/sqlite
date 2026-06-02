// Vendored from modernc.org/sqlite (see go.mod for the pin) to run against the fork in this
// repository. Build tag `modernc_upstream` keeps it out of the default
// `go test ./...` so the long-running upstream suite stays opt-in.
// See docs/modernc-upstream.md for the reproduction recipe.

//go:build modernc_upstream

// Copyright 2024 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0 license that can be
// found in the LICENSE file.

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFcntlDataVersion(t *testing.T) {
	name := filepath.Join(t.TempDir(), "tmp.db")
	db, err := sql.Open(driverName, fmt.Sprintf("file:%s", name))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	conn, err := db.Conn(context.TODO())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(context.TODO(), "create table t(v int)"); err != nil {
		t.Fatal(err)
	}

	getVersion := func() uint32 {
		t.Helper()
		var v uint32
		err = conn.Raw(func(driverConn any) error {
			fc, ok := driverConn.(FileControl)
			if !ok {
				return fmt.Errorf("driver connection didn't implement FileControl")
			}
			got, err := fc.FileControlDataVersion("main")
			v = got
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	v0 := getVersion()

	// A commit on this connection advances the version observed here.
	if _, err := conn.ExecContext(context.TODO(), "insert into t(v) values (1)"); err != nil {
		t.Fatal(err)
	}
	v1 := getVersion()
	if v1 == v0 {
		t.Errorf("data version did not change after a local write: still %d", v0)
	}

	// A commit on a different connection must also advance the version.
	other, err := db.Conn(context.TODO())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ExecContext(context.TODO(), "insert into t(v) values (2)"); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}

	// Force the original connection's pager to observe the change.
	if _, err := conn.ExecContext(context.TODO(), "select count(*) from t"); err != nil {
		t.Fatal(err)
	}

	if v := getVersion(); v == v1 {
		t.Errorf("data version did not change after a write from another connection: still %d", v)
	}
}

func TestFcntlPersistWAL(t *testing.T) {
	t.Run("WAL is cleaned up without persist WAL", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "tmp.db")
		walName := name + "-wal"
		db, err := sql.Open(driverName, fmt.Sprintf("file:%s", name))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		// enable WAL journal
		if _, err := db.Exec("pragma journal_mode = WAL"); err != nil {
			t.Fatal(err)
		}

		if _, err := db.Exec("create table t(b int)"); err != nil {
			t.Fatal(err)
		}

		// database file must exist after creating a table
		if _, err := os.Stat(name); err != nil {
			t.Fatal(err)
		}

		// wal file must exist after creating a table
		if _, err := os.Stat(walName); err != nil {
			t.Fatal(err)
		}

		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		// database file must exist after closing it
		if _, err := os.Stat(name); err != nil {
			t.Fatal(err)
		}

		// wal file must NOT exist after closing the db
		if _, err := os.Stat(walName); err == nil {
			t.Errorf("expected WAL file %s to not exist after closing db", walName)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	})

	t.Run("WAL is not cleaned up with persist WAL", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "tmp.db")
		walName := name + "-wal"
		db, err := sql.Open(driverName, fmt.Sprintf("file:%s", name))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		conn, err := db.Conn(context.TODO())
		if err != nil {
			t.Fatal(err)
		}

		// enable persist WAL for a connection, normally this is done with a hook
		err = conn.Raw(func(driverConn any) error {
			fc, ok := driverConn.(FileControl)
			if !ok {
				return fmt.Errorf("driver connection didn't implement FileControl")
			}

			// query
			mode, err := fc.FileControlPersistWAL("main", -1)
			if err != nil {
				return fmt.Errorf("file control call failed: %w", err)
			} else if mode != 0 {
				return fmt.Errorf("file control call returned unexpected mode: %d", mode)
			}

			// enable
			mode, err = fc.FileControlPersistWAL("main", 1)
			if err != nil {
				return fmt.Errorf("file control call failed: %w", err)
			} else if mode != 1 {
				return fmt.Errorf("file control call returned unexpected mode: %d", mode)
			}

			// verify
			mode, err = fc.FileControlPersistWAL("main", -1)
			if err != nil {
				return fmt.Errorf("file control call failed: %w", err)
			} else if mode != 1 {
				return fmt.Errorf("file control call returned unexpected mode: %d", mode)
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		if _, err := conn.ExecContext(context.TODO(), "pragma journal_mode = WAL"); err != nil {
			t.Fatal(err)
		}

		if _, err := conn.ExecContext(context.TODO(), "create table t(b int)"); err != nil {
			t.Fatal(err)
		}

		// database file must exist after creating a table
		if _, err := os.Stat(name); err != nil {
			t.Fatal(err)
		}

		// wal file must exist after creating a table
		if _, err := os.Stat(walName); err != nil {
			t.Fatal(err)
		}

		// close connection, should persist WAL
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}

		// close database, should persist WAL
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		// database file must exist after closing it
		if _, err := os.Stat(name); err != nil {
			t.Fatal(err)
		}

		// wal file must exist after closing the db
		if _, err := os.Stat(walName); err != nil {
			t.Errorf("expected WAL file %s to exist after closing db: %s", walName, err.Error())
		}
	})
}
