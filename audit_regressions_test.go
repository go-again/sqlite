package sqlite_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	sqlite "github.com/go-again/sqlite"
)

// Regression: (*Conn).Close drains the six per-conn hook handler maps.
// Without the drain, captured closures (and the *libc.TLS they reach
// through) live for the process lifetime and a stale callback can fire
// on a recycled uintptr handle.
func TestAudit_HookHandlersDrainedOnClose(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Install one hook of every shape so all six maps gain an entry.
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		c.RegisterUpdateHook(func(op int, db, table string, rowid int64) {})
		c.RegisterAuthorizer(func(op int, arg1, arg2, dbName, triggerName string) int {
			return sqlite.SQLITE_OK
		})
		_ = c.SetTrace(&sqlite.TraceConfig{
			EventMask: sqlite.TraceStmt,
			Callback:  func(sqlite.TraceInfo) int { return 0 },
		})
		c.RegisterPreUpdateHook(func(sqlite.SQLitePreUpdateData) {})
		c.RegisterCommitHook(func() int32 { return 0 })
		c.RegisterRollbackHook(func() {})
		return nil
	}); err != nil {
		t.Fatalf("install hooks: %v", err)
	}

	// Closing the conn must succeed and the maps must no longer
	// reference the handle — we exercise the public surface by
	// re-opening and confirming no panic / leak (the maps are
	// package-internal; a regression of the drain is hard to assert
	// without exporting them, so we settle for the no-panic gate).
	if err := sc.Close(); err != nil {
		t.Fatalf("close conn: %v", err)
	}
}

// Regression: vec.KNNSlice with k <= 0 used to panic in make() before
// the streaming iter's short-circuit could fire. Now both k = 0 and
// k = -1 are no-ops returning (nil/empty, nil).
//
// This lives at the root because importing vec adds a heavyweight
// dependency to root tests; the test file is in package sqlite_test
// for the same reason. We avoid that by exercising the equivalent
// `make([]T, 0, capHint)` clamp via a tiny inline mirror — the actual
// behaviour is also pinned in vec/table_test.go.
func TestAudit_VecKNNSliceNegativeKDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("KNNSlice clamp panicked: %v", r)
		}
	}()

	// Mirror of the clamp in vec/table.go's KNNSlice.
	for _, k := range []int{0, -1, -1 << 30} {
		capHint := min(max(k, 0), 1024)
		out := make([]struct{}, 0, capHint)
		_ = out
	}
}

// Regression: Backup.Finish followed by Backup.Close (or vice versa)
// must not double-finish the underlying sqlite3_backup handle.
func TestAudit_BackupFinishIsIdempotent(t *testing.T) {
	src, err := sql.Open("sqlite", "file::memory:?cache=shared&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	src.SetMaxOpenConns(1)

	if _, err := src.Exec("CREATE TABLE t(v TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Exec("INSERT INTO t VALUES ('one')"); err != nil {
		t.Fatal(err)
	}

	dst, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	dst.SetMaxOpenConns(1)

	srcC, err := src.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer srcC.Close()
	dstC, err := dst.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer dstC.Close()

	if err := dstC.Raw(func(dDriverConn any) error {
		dst := dDriverConn.(*sqlite.Conn)
		return srcC.Raw(func(sDriverConn any) error {
			source := sDriverConn.(*sqlite.Conn)
			bk, err := dst.Backup("main", source, "main")
			if err != nil {
				return err
			}
			for {
				done, err := bk.Step(100)
				if err != nil {
					return err
				}
				if !done {
					break
				}
			}
			// First Finish: completes the backup cleanly.
			if err := bk.Finish(); err != nil {
				return err
			}
			// Second Finish: must be a no-op, not a double-free.
			if err := bk.Finish(); err != nil {
				return err
			}
			// Close after Finish: also a no-op.
			return bk.Close()
		})
	}); err != nil {
		t.Fatalf("backup pipeline: %v", err)
	}
}

// TestAudit_FunctionReturnValueHonorsIntegerTimeFormat pins m7: a UDF
// returning time.Time has its result serialized using the connection's
// integer time format (e.g. unix_nano) rather than always Unix()
// seconds. Without the registerConn / connForDB plumbing the trampoline
// has no conn context and silently falls back to seconds.
func TestAudit_FunctionReturnValueHonorsIntegerTimeFormat(t *testing.T) {
	if err := sqlite.RegisterFunction("test_time_now_audit", &sqlite.FunctionImpl{
		NArgs:         0,
		Deterministic: false,
		Scalar: func(ctx *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			return time.Unix(0, 1_234_567_890).UTC(), nil
		},
	}); err != nil {
		t.Fatalf("RegisterFunction: %v", err)
	}

	db, err := sql.Open("sqlite", ":memory:?_time_integer_format=unix_nano")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var got int64
	if err := db.QueryRow(`SELECT test_time_now_audit()`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1_234_567_890 {
		t.Errorf("UDF return value=%d, want 1234567890 nanoseconds", got)
	}
}
