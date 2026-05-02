// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// withMattnConn opens an in-memory DB through the mattn-style driver name and
// returns a *sql.Conn pinned to the same goroutine plus a *Conn the test can
// call low-level mattn methods on.
//
// Hooks installed via the *Conn fire only for operations executed on the same
// underlying connection — i.e. via the returned *sql.Conn, not via the *sql.DB
// pool, which may pick a different physical connection on each call.
//
// For convenience, *sql.DB is also returned; tests that don't install
// per-connection hooks can use it freely.
func withMattnConn(t *testing.T, dsn string) (*sql.DB, *sql.Conn, *Conn) {
	t.Helper()
	db, err := sql.Open(DriverNameMattn, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// Restrict the pool to one connection so db.Exec/db.Query inadvertently
	// used by hook-driven tests still hit the same physical conn.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	var got *Conn
	if err := c.Raw(func(driverConn any) error {
		gc, ok := driverConn.(*Conn)
		if !ok {
			return errors.New("driverConn is not *sqlite.Conn")
		}
		got = gc
		return nil
	}); err != nil {
		t.Fatalf("Raw: %v", err)
	}
	return db, c, got
}

// TestRegisterFunc_Scalar exercises the reflective mattn-style RegisterFunc
// across the common argument and return types. UDFs registered via *Conn live
// on that connection only, so the test queries via sc.QueryRowContext.
func TestRegisterFunc_Scalar(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	ctx := context.Background()

	if err := c.RegisterFunc("addi", func(a, b int64) int64 { return a + b }, true); err != nil {
		t.Fatalf("RegisterFunc addi: %v", err)
	}
	if err := c.RegisterFunc("upper", strings.ToUpper, true); err != nil {
		t.Fatalf("RegisterFunc upper: %v", err)
	}
	if err := c.RegisterFunc("orFalse", func(b bool) bool { return b }, true); err != nil {
		t.Fatalf("RegisterFunc orFalse: %v", err)
	}

	tests := []struct {
		query string
		want  any
	}{
		{"SELECT addi(40, 2)", int64(42)},
		{"SELECT upper('hello')", "HELLO"},
		{"SELECT orFalse(1)", int64(1)},
		{"SELECT orFalse(0)", int64(0)},
	}
	for _, tt := range tests {
		var got any
		if err := sc.QueryRowContext(ctx, tt.query).Scan(&got); err != nil {
			t.Errorf("%s: %v", tt.query, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s: got %v (%T), want %v (%T)", tt.query, got, got, tt.want, tt.want)
		}
	}
}

// TestRegisterFunc_Variadic verifies that variadic Go functions work as SQLite
// user functions accepting any number of args.
func TestRegisterFunc_Variadic(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	ctx := context.Background()
	if err := c.RegisterFunc("sumAll", func(xs ...int64) int64 {
		var s int64
		for _, x := range xs {
			s += x
		}
		return s
	}, true); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		want int64
	}{
		{"SELECT sumAll()", 0},
		{"SELECT sumAll(1)", 1},
		{"SELECT sumAll(1, 2, 3, 4)", 10},
	} {
		var got int64
		if err := sc.QueryRowContext(ctx, q.sql).Scan(&got); err != nil {
			t.Errorf("%s: %v", q.sql, err)
			continue
		}
		if got != q.want {
			t.Errorf("%s = %d, want %d", q.sql, got, q.want)
		}
	}
}

// TestRegisterFunc_Errored confirms returning an error from a UDF surfaces as
// a query error, not a panic.
func TestRegisterFunc_Errored(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	ctx := context.Background()
	if err := c.RegisterFunc("boom", func() (int64, error) {
		return 0, errors.New("kaboom")
	}, true); err != nil {
		t.Fatal(err)
	}
	if err := sc.QueryRowContext(ctx, "SELECT boom()").Scan(new(int64)); err == nil {
		t.Fatal("expected error from UDF")
	} else if !strings.Contains(err.Error(), "kaboom") {
		t.Errorf("error %q does not contain 'kaboom'", err)
	}
}

// runningSum is a reflective aggregator type with the conventional Step/Done
// signatures expected by RegisterAggregator.
type runningSum struct{ total int64 }

func (r *runningSum) Step(v int64) { r.total += v }
func (r *runningSum) Done() int64  { return r.total }

func TestRegisterAggregator(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	ctx := context.Background()
	if err := c.RegisterAggregator("rsum", func() *runningSum { return &runningSum{} }, true); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t (v INTEGER); INSERT INTO t VALUES (1), (2), (3), (4), (5)`); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := sc.QueryRowContext(ctx, "SELECT rsum(v) FROM t").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 15 {
		t.Errorf("rsum=%d, want 15", got)
	}
}

func TestRegisterCollation_ReverseOrder(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	ctx := context.Background()
	if err := c.RegisterCollation("rev", func(a, b string) int {
		switch {
		case a == b:
			return 0
		case a < b:
			return 1
		default:
			return -1
		}
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := sc.ExecContext(ctx, `CREATE TABLE t (s TEXT); INSERT INTO t VALUES ('a'), ('b'), ('c')`); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(ctx, "SELECT s FROM t ORDER BY s COLLATE rev")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	want := []string{"c", "b", "a"}
	for i, s := range want {
		if got[i] != s {
			t.Errorf("[%d] got %q, want %q", i, got[i], s)
		}
	}
}

// TestUpdateHook_FiresOnInsertUpdateDelete checks all three op codes arrive at
// the callback in the right order with the expected payloads.
//
// Update hooks are per-connection, so exec runs on the pinned *sql.Conn.
func TestUpdateHook_FiresOnInsertUpdateDelete(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")

	type event struct {
		op    int
		db    string
		table string
		rowid int64
	}
	var events []event

	c.RegisterUpdateHook(func(op int, dbName, table string, rowid int64) {
		events = append(events, event{op, dbName, table, rowid})
	})
	defer c.RegisterUpdateHook(nil)

	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, v INTEGER);
INSERT INTO t (id, v) VALUES (1, 100);
UPDATE t SET v = 200 WHERE id = 1;
DELETE FROM t WHERE id = 1;`); err != nil {
		t.Fatal(err)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(events), events)
	}
	want := []event{
		{SQLITE_INSERT, "main", "t", 1},
		{SQLITE_UPDATE, "main", "t", 1},
		{SQLITE_DELETE, "main", "t", 1},
	}
	for i, w := range want {
		if events[i] != w {
			t.Errorf("event[%d] = %+v, want %+v", i, events[i], w)
		}
	}
}

// TestAuthorizer_DenyReadColumn shows that returning SQLITE_DENY surfaces as a
// statement-prep error citing the denied resource.
func TestAuthorizer_DenyReadColumn(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	ctx := context.Background()

	if _, err := sc.ExecContext(ctx, `CREATE TABLE t (id INTEGER, secret TEXT)`); err != nil {
		t.Fatal(err)
	}
	c.RegisterAuthorizer(func(op int, a, b, dbName, trigger string) int {
		if op == SQLITE_READ && a == "t" && b == "secret" {
			return SQLITE_DENY
		}
		return SQLITE_OK
	})
	defer c.RegisterAuthorizer(nil)

	rows, err := sc.QueryContext(ctx, "SELECT secret FROM t")
	if err == nil {
		rows.Close()
		t.Fatal("expected denial, got nil")
	} else if !strings.Contains(err.Error(), "secret") && !strings.Contains(err.Error(), "AUTH") {
		t.Errorf("error %q does not reference the denied column/auth", err)
	}

	// Non-denied column still works.
	rows, err = sc.QueryContext(ctx, "SELECT id FROM t")
	if err != nil {
		t.Errorf("non-denied column unexpectedly failed: %v", err)
	} else {
		rows.Close()
	}
}

// TestAuthorizer_IgnoreColumnReturnsNull verifies SQLITE_IGNORE returns NULL
// in place of the would-be-read column value.
func TestAuthorizer_IgnoreColumnReturnsNull(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	ctx := context.Background()

	if _, err := sc.ExecContext(ctx, `CREATE TABLE t (id INTEGER, secret TEXT); INSERT INTO t VALUES (1, 'shh')`); err != nil {
		t.Fatal(err)
	}
	c.RegisterAuthorizer(func(op int, a, b, dbName, trigger string) int {
		if op == SQLITE_READ && b == "secret" {
			return SQLITE_IGNORE
		}
		return SQLITE_OK
	})
	defer c.RegisterAuthorizer(nil)

	var v sql.NullString
	if err := sc.QueryRowContext(ctx, "SELECT secret FROM t").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v.Valid {
		t.Errorf("expected NULL, got %q", v.String)
	}
}

// TestCommitRollbackHook_Fire makes sure the per-connection commit/rollback
// hooks actually fire on the expected transaction events.
//
// Note: hooks are per physical connection, so the test exercises them via
// sc.ExecContext on the *sql.Conn that the hooks were installed on, not via
// db.Exec which goes through the pool.
func TestCommitRollbackHook_Fire(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")

	var commits, rollbacks int32
	c.RegisterCommitHook(func() int32 { atomic.AddInt32(&commits, 1); return 0 })
	defer c.RegisterCommitHook(nil)
	c.RegisterRollbackHook(func() { atomic.AddInt32(&rollbacks, 1) })
	defer c.RegisterRollbackHook(nil)

	ctx := context.Background()
	// One autocommit transaction (commit #1).
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	// Explicit transaction that commits (commit #2).
	if _, err := sc.ExecContext(ctx, `BEGIN; INSERT INTO t VALUES (1); COMMIT;`); err != nil {
		t.Fatal(err)
	}
	// Explicit transaction that rolls back (rollback #1).
	if _, err := sc.ExecContext(ctx, `BEGIN; INSERT INTO t VALUES (2); ROLLBACK;`); err != nil {
		t.Fatal(err)
	}

	if got := atomic.LoadInt32(&commits); got != 2 {
		t.Errorf("commit hook fired %d times, want 2", got)
	}
	if got := atomic.LoadInt32(&rollbacks); got != 1 {
		t.Errorf("rollback hook fired %d times, want 1", got)
	}
}

// TestSetTrace_StmtEventReceived asserts that TraceStmt events deliver the SQL
// text to the registered callback.
//
// Trace handlers are per-connection; the test exercises them on the pinned
// *sql.Conn the trace handler was installed on.
func TestSetTrace_StmtEventReceived(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")

	var got []string
	if err := c.SetTrace(&TraceConfig{
		EventMask: TraceStmt,
		Callback:  func(info TraceInfo) int { got = append(got, info.Statement); return 0 },
	}); err != nil {
		t.Fatal(err)
	}
	defer c.SetTrace(nil)

	if _, err := sc.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("trace callback never fired")
	}
	if !strings.Contains(got[0], "SELECT 1") {
		t.Errorf("trace text %q does not contain SELECT 1", got[0])
	}
}

// TestError_CodeAndExtendedCode_OnUniqueViolation ensures the inserted UNIQUE
// constraint surfaces both the primary and extended SQLite codes.
func TestError_CodeAndExtendedCode_OnUniqueViolation(t *testing.T) {
	db, err := sql.Open(DriverNameMattn, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT UNIQUE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t (id, name) VALUES (1, 'a')`); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO t (id, name) VALUES (2, 'a')`)
	if err == nil {
		t.Fatal("expected unique violation")
	}
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("error not *sqlite.Error: %T", err)
	}
	if se.Code() != SQLITE_CONSTRAINT {
		t.Errorf("Code()=%d, want SQLITE_CONSTRAINT=%d", se.Code(), SQLITE_CONSTRAINT)
	}
	if se.ExtendedCode() != SQLITE_CONSTRAINT_UNIQUE {
		t.Errorf("ExtendedCode()=%d, want SQLITE_CONSTRAINT_UNIQUE=%d", se.ExtendedCode(), SQLITE_CONSTRAINT_UNIQUE)
	}
	if !errors.Is(err, ErrConstraint) {
		t.Errorf("errors.Is(err, ErrConstraint)=false, want true")
	}
	if !errors.Is(err, ErrConstraintUnique) {
		t.Errorf("errors.Is(err, ErrConstraintUnique)=false, want true")
	}
}

// TestGetLimit_RoundTrip cycles SetLimit/GetLimit through a known limit id and
// confirms the value reads back identically.
func TestGetLimit_RoundTrip(t *testing.T) {
	_, _, c := withMattnConn(t, ":memory:")

	const id = SQLITE_LIMIT_LENGTH
	orig := c.GetLimit(id)
	prev := c.SetLimit(id, orig/2)
	if prev != orig {
		t.Errorf("SetLimit returned %d, want %d", prev, orig)
	}
	if cur := c.GetLimit(id); cur != orig/2 {
		t.Errorf("GetLimit after set = %d, want %d", cur, orig/2)
	}
	// Restore.
	c.SetLimit(id, orig)
}

// TestMattnDriverLiteral exercises the mattn idiom of registering a custom
// driver name via &sqlite3.SQLiteDriver{ConnectHook: ...}.
func TestMattnDriverLiteral(t *testing.T) {
	const custom = "go-again-custom-1"
	var hookFired int32
	sql.Register(custom, &SQLiteDriver{
		ConnectHook: func(c *SQLiteConn) error {
			atomic.AddInt32(&hookFired, 1)
			return c.RegisterFunc("answer", func() int64 { return 42 }, true)
		},
	})
	db, err := sql.Open(custom, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int64
	if err := db.QueryRow("SELECT answer()").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Errorf("answer()=%d, want 42", got)
	}
	if atomic.LoadInt32(&hookFired) == 0 {
		t.Errorf("ConnectHook never fired")
	}
}
