package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

const usersSchema = `CREATE TABLE users(id INTEGER PRIMARY KEY, name TEXT)`

// newSessionDB returns an independent in-memory database (its own :memory:
// conn) with the users table created, plus the pinned *sql.Conn and *Conn.
func newSessionDB(t *testing.T) (context.Context, *sql.Conn, *Conn) {
	t.Helper()
	ctx := context.Background()
	_, sc, c := withSQLite3Conn(t, ":memory:")
	if _, err := sc.ExecContext(ctx, usersSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return ctx, sc, c
}

func countUsers(t *testing.T, ctx context.Context, sc *sql.Conn) int {
	t.Helper()
	var n int
	if err := sc.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// captureUsers records two inserts + an update on src and returns the changeset.
func captureUsers(t *testing.T, ctx context.Context, srcSC *sql.Conn, src *Conn) []byte {
	t.Helper()
	sess, err := src.CreateSession("main")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	defer sess.Close()
	if err := sess.Attach(""); err != nil { // all tables
		t.Fatalf("Attach: %v", err)
	}
	if _, err := srcSC.ExecContext(ctx, `INSERT INTO users VALUES (1, 'alice'), (2, 'bob')`); err != nil {
		t.Fatal(err)
	}
	if _, err := srcSC.ExecContext(ctx, `UPDATE users SET name = 'bobby' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if sess.IsEmpty() {
		t.Fatal("session should not be empty after changes")
	}
	cs, err := sess.Changeset()
	if err != nil {
		t.Fatalf("Changeset: %v", err)
	}
	if len(cs) == 0 {
		t.Fatal("changeset is empty")
	}
	return cs
}

func TestSession_CaptureAndApply(t *testing.T) {
	ctx, srcSC, src := newSessionDB(t)
	_, dstSC, dst := newSessionDB(t)

	cs := captureUsers(t, ctx, srcSC, src)
	if err := dst.ApplyChangeset(cs); err != nil {
		t.Fatalf("ApplyChangeset: %v", err)
	}

	if n := countUsers(t, ctx, dstSC); n != 2 {
		t.Errorf("dst user count = %d, want 2", n)
	}
	var name string
	if err := dstSC.QueryRowContext(ctx, `SELECT name FROM users WHERE id = 2`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "bobby" {
		t.Errorf("dst users.id=2 name = %q, want bobby", name)
	}
}

func TestSession_Invert(t *testing.T) {
	ctx, srcSC, src := newSessionDB(t)
	_, dstSC, dst := newSessionDB(t)

	cs := captureUsers(t, ctx, srcSC, src)
	if err := dst.ApplyChangeset(cs); err != nil {
		t.Fatalf("apply: %v", err)
	}
	inv, err := src.InvertChangeset(cs)
	if err != nil {
		t.Fatalf("InvertChangeset: %v", err)
	}
	if err := dst.ApplyChangeset(inv); err != nil {
		t.Fatalf("apply inverse: %v", err)
	}
	if n := countUsers(t, ctx, dstSC); n != 0 {
		t.Errorf("after applying inverse, dst count = %d, want 0", n)
	}
}

func TestSession_ConflictReplace(t *testing.T) {
	ctx, srcSC, src := newSessionDB(t)
	_, dstSC, dst := newSessionDB(t)

	cs := captureUsers(t, ctx, srcSC, src)
	// Pre-seed a conflicting row (same PK, different value).
	if _, err := dstSC.ExecContext(ctx, `INSERT INTO users VALUES (1, 'preexisting')`); err != nil {
		t.Fatal(err)
	}

	var sawConflict ConflictType = -1
	err := dst.ApplyChangeset(cs, WithConflictHandler(func(ct ConflictType) ConflictAction {
		sawConflict = ct
		return ChangesetReplace
	}))
	if err != nil {
		t.Fatalf("ApplyChangeset with replace: %v", err)
	}
	if sawConflict != ConflictConflict {
		t.Errorf("conflict type = %v, want conflict (duplicate PK)", sawConflict)
	}
	var name string
	if err := dstSC.QueryRowContext(ctx, `SELECT name FROM users WHERE id = 1`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "alice" {
		t.Errorf("after REPLACE, users.id=1 name = %q, want alice", name)
	}
}

func TestSession_ConflictAbortByDefault(t *testing.T) {
	ctx, srcSC, src := newSessionDB(t)
	_, dstSC, dst := newSessionDB(t)

	cs := captureUsers(t, ctx, srcSC, src)
	if _, err := dstSC.ExecContext(ctx, `INSERT INTO users VALUES (1, 'preexisting')`); err != nil {
		t.Fatal(err)
	}
	// No conflict handler → the duplicate-PK conflict aborts and rolls back.
	if err := dst.ApplyChangeset(cs); err == nil {
		t.Error("ApplyChangeset with a conflict and no handler should error")
	}
	if name := ""; dstSC.QueryRowContext(ctx, `SELECT name FROM users WHERE id = 1`).Scan(&name) == nil && name != "preexisting" {
		t.Errorf("rollback failed: users.id=1 name = %q, want preexisting", name)
	}
}

func TestSession_Patchset(t *testing.T) {
	ctx, srcSC, src := newSessionDB(t)
	_, dstSC, dst := newSessionDB(t)

	sess, err := src.CreateSession("main")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Attach("users"); err != nil {
		t.Fatal(err)
	}
	if _, err := srcSC.ExecContext(ctx, `INSERT INTO users VALUES (1, 'alice')`); err != nil {
		t.Fatal(err)
	}
	ps, err := sess.Patchset()
	if err != nil {
		t.Fatalf("Patchset: %v", err)
	}
	if len(ps) == 0 {
		t.Fatal("patchset is empty")
	}
	if err := dst.ApplyChangeset(ps); err != nil {
		t.Fatalf("apply patchset: %v", err)
	}
	if n := countUsers(t, ctx, dstSC); n != 1 {
		t.Errorf("dst count after patchset = %d, want 1", n)
	}
}

func TestSession_TableFilter(t *testing.T) {
	ctx, srcSC, src := newSessionDB(t)
	_, dstSC, dst := newSessionDB(t)

	cs := captureUsers(t, ctx, srcSC, src)
	// Filter excludes "users" → nothing applied.
	err := dst.ApplyChangeset(cs, WithTableFilter(func(table string) bool {
		return table != "users"
	}))
	if err != nil {
		t.Fatalf("ApplyChangeset with filter: %v", err)
	}
	if n := countUsers(t, ctx, dstSC); n != 0 {
		t.Errorf("filtered-out table applied %d rows, want 0", n)
	}
}

func TestSession_EnableDisable(t *testing.T) {
	ctx, srcSC, src := newSessionDB(t)

	sess, err := src.CreateSession("main")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Attach("users"); err != nil {
		t.Fatal(err)
	}
	if !sess.IsEnabled() {
		t.Error("session should start enabled")
	}
	// Disabled changes are not recorded.
	sess.Enable(false)
	if sess.IsEnabled() {
		t.Error("IsEnabled should be false after Enable(false)")
	}
	if _, err := srcSC.ExecContext(ctx, `INSERT INTO users VALUES (1, 'ignored')`); err != nil {
		t.Fatal(err)
	}
	if !sess.IsEmpty() {
		t.Error("disabled session recorded a change")
	}
	// Re-enabled changes are recorded.
	sess.Enable(true)
	if _, err := srcSC.ExecContext(ctx, `INSERT INTO users VALUES (2, 'tracked')`); err != nil {
		t.Fatal(err)
	}
	if sess.IsEmpty() {
		t.Error("re-enabled session recorded nothing")
	}
}
