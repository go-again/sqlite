// Vendored from modernc.org/sqlite (see go.mod for the pin) to run against the fork in this
// repository. Build tag `modernc_upstream` keeps it out of the default
// `go test ./...` so the long-running upstream suite stays opt-in.
// See docs/modernc-upstream.md for the reproduction recipe.

//go:build modernc_upstream

package sqlite

import (
	"database/sql"
	"fmt"
	"slices"
	"testing"

	sqlite3 "modernc.org/sqlite/lib"
)

func TestPreUpdateHook(t *testing.T) {
	connStr := ":memory:"
	driverName := "sqlite_pre_update_hook_test"

	var (
		insertCount     int
		insertNewValues []any
		updateCount     int
		updateOldValues []any
		updateNewValues []any
		deleteCount     int
		deleteOldValues []any
		commitCount     int
		rollbackCount   int
	)
	var testDriver Driver
	testDriver.RegisterConnectionHook(func(conn ExecQuerierContext, dsn string) error {
		if hooker, ok := conn.(HookRegisterer); ok {
			hooker.RegisterPreUpdateHook(func(data SQLitePreUpdateData) {
				switch data.Op {
				case sqlite3.SQLITE_INSERT:
					insertCount++
					insertNewValues = make([]any, data.Count())
					err := data.New(insertNewValues...)
					if err != nil {
						t.Fatal(err)
					}
				case sqlite3.SQLITE_UPDATE:
					updateCount++
					updateOldValues = make([]any, data.Count())
					err := data.Old(updateOldValues...)
					if err != nil {
						t.Fatal(err)
					}
					updateNewValues = make([]any, data.Count())
					err = data.New(updateNewValues...)
					if err != nil {
						t.Fatal(err)
					}
				case sqlite3.SQLITE_DELETE:
					deleteCount++
					deleteOldValues = make([]any, data.Count())
					err := data.Old(deleteOldValues...)
					if err != nil {
						t.Fatal(err)
					}
				}
			})
			hooker.RegisterCommitHook(func() int32 {
				commitCount++
				return 0
			})
			hooker.RegisterRollbackHook(func() {
				rollbackCount++
			})
		}
		return nil
	})

	sql.Register(driverName, &testDriver)

	db, err := sql.Open(driverName, connStr)
	if err != nil {
		t.Fatal(err)
	}

	expectInsertValues := []any{int64(42), 3.1415, "Test", "will be nil"}
	expectUpdateValues := []any{int64(43), 1.5, "Test update", nil}
	_, err = db.Exec(`
	CREATE TABLE in_memory_test(id INTEGER PRIMARY KEY, f FLOAT, t TEXT, x ANY);
	INSERT INTO in_memory_test VALUES(42, 3.1415, 'Test', 'will be nil');
	UPDATE in_memory_test SET id = 43, f = 1.5, t = 'Test update', x = null;
	DELETE FROM in_memory_test;
	SELECT 1;
	`)
	if err != nil {
		t.Fatal(err)
	}
	if insertCount != 1 {
		t.Errorf("pre update hook: expect %d inserts call, got %d", 1, insertCount)
	}
	if !slices.Equal(insertNewValues, expectInsertValues) {
		t.Errorf("pre update hook: expect %v as inserted new values, got %v", expectInsertValues, insertNewValues)
	}
	if updateCount != 1 {
		t.Errorf("pre update hook: expect %d updates call, got %d", 1, updateCount)
	}
	if !slices.Equal(updateOldValues, expectInsertValues) {
		t.Errorf("pre update hook: expect %v as updated old values, got %v", expectInsertValues, updateOldValues)
	}
	if !slices.Equal(updateNewValues, expectUpdateValues) {
		t.Errorf("pre update hook: expect %v as updated new values, got %v", expectUpdateValues, updateNewValues)
	}
	if deleteCount != 1 {
		t.Errorf("pre update hook: expect %d deletes call, got %d", 1, deleteCount)
	}
	if !slices.Equal(deleteOldValues, expectUpdateValues) {
		t.Errorf("pre update hook: expect %v as deleted old values, got %v", expectUpdateValues, deleteOldValues)
	}
	if commitCount != 4 {
		t.Errorf("commit hook: expect %d, got %d", 4, commitCount)
	}
	if rollbackCount != 0 {
		t.Errorf("rollback hook: expect %d, got %d", 0, rollbackCount)
	}

}

func TestPreUpdateHookLargeRowID(t *testing.T) {
	driverName := fmt.Sprintf("sqlite_preupdate_large_rowid_%p", t)
	var gotOldRowID, gotNewRowID int64
	var gotOp int32

	var testDriver Driver
	testDriver.RegisterConnectionHook(func(conn ExecQuerierContext, dsn string) error {
		if hooker, ok := conn.(HookRegisterer); ok {
			hooker.RegisterPreUpdateHook(func(data SQLitePreUpdateData) {
				gotOp = data.Op
				gotOldRowID = data.OldRowID
				gotNewRowID = data.NewRowID
			})
		}
		return nil
	})
	sql.Register(driverName, &testDriver)

	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`)
	if err != nil {
		t.Fatal(err)
	}

	const largeRowID = int64(1) << 40 // 1 TiB — well beyond int32 range
	_, err = db.Exec(`INSERT INTO t VALUES(?, 'hello')`, largeRowID)
	if err != nil {
		t.Fatal(err)
	}

	if gotOp != sqlite3.SQLITE_INSERT {
		t.Fatalf("expected INSERT op, got %d", gotOp)
	}
	if gotNewRowID != largeRowID {
		t.Errorf("NewRowID: got %d, want %d", gotNewRowID, largeRowID)
	}

	// Also test UPDATE with large rowids
	const newLargeRowID = int64(1)<<40 + 1
	gotOldRowID = 0
	gotNewRowID = 0
	_, err = db.Exec(`UPDATE t SET id = ?, v = 'updated' WHERE id = ?`, newLargeRowID, largeRowID)
	if err != nil {
		t.Fatal(err)
	}

	if gotOp != sqlite3.SQLITE_UPDATE {
		t.Fatalf("expected UPDATE op, got %d", gotOp)
	}
	if gotOldRowID != largeRowID {
		t.Errorf("OldRowID after UPDATE: got %d, want %d", gotOldRowID, largeRowID)
	}
	if gotNewRowID != newLargeRowID {
		t.Errorf("NewRowID after UPDATE: got %d, want %d", gotNewRowID, newLargeRowID)
	}
}
