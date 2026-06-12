package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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

func TestSession_Diff(t *testing.T) {
	ctx, srcSC, src := newSessionDB(t) // main.users created
	if _, err := srcSC.ExecContext(ctx, `INSERT INTO users VALUES (1, 'alice')`); err != nil {
		t.Fatal(err)
	}
	// A second in-memory database 'aux' with the same table but extra data.
	if _, err := srcSC.ExecContext(ctx, `ATTACH DATABASE ':memory:' AS aux`); err != nil {
		t.Fatal(err)
	}
	if _, err := srcSC.ExecContext(ctx, `CREATE TABLE aux.users(id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := srcSC.ExecContext(ctx, `INSERT INTO aux.users VALUES (1, 'alice'), (2, 'bob')`); err != nil {
		t.Fatal(err)
	}

	sess, err := src.CreateSession("main")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Attach("users"); err != nil {
		t.Fatal(err)
	}
	// Diff captures the changes that make aux.users match main.users.
	if err := sess.Diff("aux", "users"); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	cs, err := sess.Changeset()
	if err != nil {
		t.Fatalf("Changeset after Diff: %v", err)
	}
	if len(cs) == 0 {
		t.Error("Diff of differing tables produced an empty changeset")
	}

	// Error path: a schema mismatch (different column count) makes Diff fail
	// with an error message, exercising the pErr branch and its sqlite3_free.
	if _, err := srcSC.ExecContext(ctx, `CREATE TABLE mism(id INTEGER PRIMARY KEY, a TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := srcSC.ExecContext(ctx, `CREATE TABLE aux.mism(id INTEGER PRIMARY KEY, a TEXT, b TEXT)`); err != nil {
		t.Fatal(err)
	}
	sess2, err := src.CreateSession("main")
	if err != nil {
		t.Fatal(err)
	}
	defer sess2.Close()
	if err := sess2.Attach("mism"); err != nil {
		t.Fatal(err)
	}
	// The pErr branch must surface a *sqlite.Error carrying SQLite's own
	// diagnostic (copied out of the C pzErrMsg buffer before sqlite3_free),
	// not a generic result-code error.
	err = sess2.Diff("aux", "mism")
	var serr *Error
	if !errors.As(err, &serr) {
		t.Fatalf("Diff between schema-mismatched tables: error = %v (%T), want *sqlite.Error", err, err)
	}
	if !strings.Contains(serr.Error(), "table schemas do not match") {
		t.Errorf("Diff mismatch error = %q, want it to mention %q", serr.Error(), "table schemas do not match")
	}
}

func TestSession_Concat(t *testing.T) {
	ctx, srcSC, src := newSessionDB(t)
	_, dstSC, dst := newSessionDB(t)

	// First changeset: insert alice.
	sess1, err := src.CreateSession("main")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess1.Attach("")
	if _, err := srcSC.ExecContext(ctx, `INSERT INTO users VALUES (1, 'alice')`); err != nil {
		t.Fatal(err)
	}
	cs1, err := sess1.Changeset()
	if err != nil {
		t.Fatal(err)
	}
	sess1.Close()

	// Second changeset: insert bob.
	sess2, err := src.CreateSession("main")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess2.Attach("")
	if _, err := srcSC.ExecContext(ctx, `INSERT INTO users VALUES (2, 'bob')`); err != nil {
		t.Fatal(err)
	}
	cs2, err := sess2.Changeset()
	if err != nil {
		t.Fatal(err)
	}
	sess2.Close()

	combined, err := src.ConcatChangesets(cs1, cs2)
	if err != nil {
		t.Fatalf("ConcatChangesets: %v", err)
	}
	if err := dst.ApplyChangeset(combined); err != nil {
		t.Fatalf("apply combined: %v", err)
	}
	if n := countUsers(t, ctx, dstSC); n != 2 {
		t.Errorf("after concat+apply, count = %d, want 2", n)
	}

	// An empty operand is legitimate: concat(cs1, nil) must yield exactly cs1's
	// effect. Applying it to a fresh database inserts the single row, proving
	// the non-empty operand survives rather than being silently dropped.
	onlyCS1, err := src.ConcatChangesets(cs1, nil)
	if err != nil {
		t.Fatalf("ConcatChangesets with an empty operand: %v", err)
	}
	_, freshSC, fresh := newSessionDB(t)
	if err := fresh.ApplyChangeset(onlyCS1); err != nil {
		t.Fatalf("apply concat(cs1, nil): %v", err)
	}
	if n := countUsers(t, ctx, freshSC); n != 1 {
		t.Errorf("after concat(cs1,nil)+apply, count = %d, want 1", n)
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
