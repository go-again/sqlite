package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestRegisterBusyHandler drives real lock contention: connection A holds a
// write lock while connection B — with a busy handler installed — attempts a
// write. The handler must be called (with an increasing attempt count) and,
// once it gives up, the write must fail.
func TestRegisterBusyHandler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	ctx := context.Background()

	dbA, err := sql.Open(DriverNameSQLite3, path)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbA.SetMaxOpenConns(1)
	if _, err := dbA.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	// Hold a write lock on A: the INSERT inside the open transaction takes a
	// RESERVED lock that A keeps until the tx ends.
	txA, err := dbA.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer txA.Rollback()
	if _, err := txA.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}

	dbB, err := sql.Open(DriverNameSQLite3, path)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	dbB.SetMaxOpenConns(1)
	scB, err := dbB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer scB.Close()

	var maxAttempts atomic.Int64
	if err := scB.Raw(func(dc any) error {
		dc.(*Conn).RegisterBusyHandler(func(attempts int) bool {
			maxAttempts.Store(int64(attempts))
			return attempts < 3 // retry a few times, then give up
		})
		return nil
	}); err != nil {
		t.Fatalf("install busy handler: %v", err)
	}

	// B's write contends with A's held lock → the busy handler fires, then gives
	// up at attempt 3, so the write fails.
	if _, err := scB.ExecContext(ctx, `INSERT INTO t VALUES (2)`); err == nil {
		t.Error("expected the write to fail (SQLITE_BUSY) once the handler gave up")
	}
	if got := maxAttempts.Load(); got < 3 {
		t.Errorf("busy handler reached attempt %d, want >= 3 (it should have been retried)", got)
	}

	// Removing the handler is a no-op-safe call.
	if err := scB.Raw(func(dc any) error {
		dc.(*Conn).RegisterBusyHandler(nil)
		return nil
	}); err != nil {
		t.Fatalf("clear busy handler: %v", err)
	}
}
