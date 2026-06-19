package vec_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/vec"
)

// TestErrAlreadyExists_StableMessageFragment pins the upstream message
// fragment our isAlreadyExistsErr matcher relies on. The matcher does
// a case-insensitive Contains check on `err.Error()` for "already
// exists". If SQLite's CREATE-on-existing-table error wording ever
// drifts away from that fragment, this test catches it before users
// see vec.ErrAlreadyExists silently stop firing.
//
// Uses a fresh shared-memory DSN (no MaxOpenConns pin) because
// vec.Create opens its own pool conn and would deadlock against a
// caller-held single conn.
func TestErrAlreadyExists_StableMessageFragment(t *testing.T) {
	db, err := sql.Open(sqlite.DriverName, "file:vec_exists_pin?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	const name = "exists_pin"
	if _, err := vec.Create(ctx, db, name, 4, vec.Options{}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err = vec.Create(ctx, db, name, 4, vec.Options{})
	if err == nil {
		t.Fatal("second Create on same name: want error, got nil")
	}
	if !errors.Is(err, vec.ErrAlreadyExists) {
		t.Fatalf("second Create error %q does NOT wrap ErrAlreadyExists; "+
			"the upstream SQLite message fragment 'already exists' may have changed", err)
	}
}
