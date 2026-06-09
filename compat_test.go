package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// withSQLite3Conn opens an in-memory DB through the mattn-style driver name and
// returns a *sql.Conn pinned to the same goroutine plus a *Conn the test can
// call low-level mattn methods on.
//
// Hooks installed via the *Conn fire only for operations executed on the same
// underlying connection — i.e. via the returned *sql.Conn, not via the *sql.DB
// pool, which may pick a different physical connection on each call.
//
// For convenience, *sql.DB is also returned; tests that don't install
// per-connection hooks can use it freely.
func withSQLite3Conn(t *testing.T, dsn string) (*sql.DB, *sql.Conn, *Conn) {
	t.Helper()
	db, err := sql.Open(DriverNameSQLite3, dsn)
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
	_, sc, c := withSQLite3Conn(t, ":memory:")
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

// TestRegisterFunc_EmbeddedNUL pins that a TEXT argument carrying an embedded
// NUL byte reaches the Go function intact, and that a string result with an
// embedded NUL round-trips. functionArgs used to read text args via
// libc.GoString, which truncated at the first NUL ("foo\x00bar" → "foo"); the
// fix reads the explicit byte length after calling the text accessor.
func TestRegisterFunc_EmbeddedNUL(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()

	if err := c.RegisterFunc("arglen", func(s string) int64 { return int64(len(s)) }, true); err != nil {
		t.Fatalf("RegisterFunc arglen: %v", err)
	}
	if err := c.RegisterFunc("echo", func(s string) string { return s }, true); err != nil {
		t.Fatalf("RegisterFunc echo: %v", err)
	}

	const text = "foo\x00bar" // 7 bytes, embedded NUL in the middle

	var n int64
	if err := sc.QueryRowContext(ctx, "SELECT arglen(?)", text).Scan(&n); err != nil {
		t.Fatalf("arglen: %v", err)
	}
	if n != int64(len(text)) {
		t.Errorf("arglen = %d, want %d (TEXT arg truncated at embedded NUL)", n, len(text))
	}

	var got string
	if err := sc.QueryRowContext(ctx, "SELECT echo(?)", text).Scan(&got); err != nil {
		t.Fatalf("echo: %v", err)
	}
	if got != text {
		t.Errorf("echo round-trip = %q, want %q", got, text)
	}
}

// TestRegisterFunc_Variadic verifies that variadic Go functions work as SQLite
// user functions accepting any number of args.
func TestRegisterFunc_Variadic(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
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
	_, sc, c := withSQLite3Conn(t, ":memory:")
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
	_, sc, c := withSQLite3Conn(t, ":memory:")
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

// TestRegisterAggregator_WindowRecomputes documents that reflective
// aggregates registered via RegisterAggregator work even in window-function
// (OVER) contexts — SQLite falls back to recomputing each window from
// scratch when our reflectAggregate's WindowInverse returns an error,
// which is correct (if O(N²)).
//
// Asserting the behavior here so a future "optimization" that decides to
// short-circuit and error out instead doesn't regress users that already
// rely on this fallback. True linear-time window support would require
// callers to use the lower-level RegisterFunction(name, &FunctionImpl{...})
// path with an AggregateFunction that implements WindowInverse.
func TestRegisterAggregator_WindowRecomputes(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if err := c.RegisterAggregator("rsum2", func() *runningSum { return &runningSum{} }, true); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t (v INTEGER); INSERT INTO t VALUES (1), (2), (3)`); err != nil {
		t.Fatal(err)
	}

	// Non-window form: classic aggregate, returns the sum.
	var sum int64
	if err := sc.QueryRowContext(ctx, "SELECT rsum2(v) FROM t").Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum != 6 {
		t.Errorf("non-window rsum2 = %d, want 6", sum)
	}

	// Window form over ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
	// (a running total). SQLite recomputes each window because
	// reflectAggregate refuses WindowInverse. Expected sums: 1, 3, 6.
	rows, err := sc.QueryContext(ctx,
		"SELECT rsum2(v) OVER (ORDER BY v ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	want := []int64{1, 3, 6}
	if len(got) != len(want) {
		t.Fatalf("got %d window rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("window[%d] = %d, want %d (full=%v)", i, got[i], w, got)
		}
	}
}

func TestRegisterCollation_ReverseOrder(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
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
	_, sc, c := withSQLite3Conn(t, ":memory:")

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
	_, sc, c := withSQLite3Conn(t, ":memory:")
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
	_, sc, c := withSQLite3Conn(t, ":memory:")
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
	_, sc, c := withSQLite3Conn(t, ":memory:")

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
	_, sc, c := withSQLite3Conn(t, ":memory:")

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

// TestSetTrace_ProfileEventReceived asserts the TraceProfile mask delivers a
// profile event with a non-zero Duration. Profile events fire AFTER the
// statement finishes (so timing is captured), unlike TraceStmt which fires
// at start.
func TestSetTrace_ProfileEventReceived(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")

	type profile struct {
		stmt string
		dur  time.Duration
	}
	var got []profile
	if err := c.SetTrace(&TraceConfig{
		EventMask: TraceProfile,
		Callback: func(info TraceInfo) int {
			got = append(got, profile{stmt: info.Statement, dur: info.Duration})
			return 0
		},
	}); err != nil {
		t.Fatal(err)
	}
	defer c.SetTrace(nil)

	// Force enough work that even fast hardware reports Duration > 0.
	// 1k rows can complete inside the trace timer's resolution and report
	// 0; 100k is ~5ms on Apple M4.
	if _, err := sc.ExecContext(context.Background(),
		`WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n<100000) SELECT count(*) FROM c`); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("profile callback never fired")
	}
	// Each event has a Duration > 0. (Some SQLite builds report >= 0 for
	// extremely short queries; we expect > 0 because our CTE does real work.)
	for i, p := range got {
		if p.dur <= 0 {
			t.Errorf("profile[%d] Duration=%v, want > 0", i, p.dur)
		}
		if p.stmt == "" {
			t.Errorf("profile[%d] Statement empty, want non-empty SQL", i)
		}
	}
}

// TestError_CodeAndExtendedCode_OnUniqueViolation ensures the inserted UNIQUE
// constraint surfaces both the primary and extended SQLite codes.
func TestError_CodeAndExtendedCode_OnUniqueViolation(t *testing.T) {
	db, err := sql.Open(DriverNameSQLite3, ":memory:")
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

// TestGetLimit_RoundTrip cycles SetLimit/GetLimit through every documented
// SQLITE_LIMIT_* identifier. For each one:
//   - GetLimit returns a sensible default (≥ 0).
//   - SetLimit halves it and returns the prior value.
//   - GetLimit afterwards reflects the new value.
//   - Restore the original so later tests aren't tripped up.
//
// SQLite caps each limit at a per-id hard maximum; setting beyond it clamps,
// so we deliberately reduce rather than increase to avoid clamp surprises.
func TestGetLimit_RoundTrip(t *testing.T) {
	limits := []struct {
		id   int
		name string
	}{
		{SQLITE_LIMIT_LENGTH, "LENGTH"},
		{SQLITE_LIMIT_SQL_LENGTH, "SQL_LENGTH"},
		{SQLITE_LIMIT_COLUMN, "COLUMN"},
		{SQLITE_LIMIT_EXPR_DEPTH, "EXPR_DEPTH"},
		{SQLITE_LIMIT_COMPOUND_SELECT, "COMPOUND_SELECT"},
		{SQLITE_LIMIT_VDBE_OP, "VDBE_OP"},
		{SQLITE_LIMIT_FUNCTION_ARG, "FUNCTION_ARG"},
		{SQLITE_LIMIT_ATTACHED, "ATTACHED"},
		{SQLITE_LIMIT_LIKE_PATTERN_LENGTH, "LIKE_PATTERN_LENGTH"},
		{SQLITE_LIMIT_VARIABLE_NUMBER, "VARIABLE_NUMBER"},
		{SQLITE_LIMIT_TRIGGER_DEPTH, "TRIGGER_DEPTH"},
		{SQLITE_LIMIT_WORKER_THREADS, "WORKER_THREADS"},
	}
	for _, lim := range limits {
		t.Run(lim.name, func(t *testing.T) {
			_, _, c := withSQLite3Conn(t, ":memory:")
			orig := c.GetLimit(lim.id)
			if orig < 0 {
				t.Fatalf("GetLimit(%s) returned %d, want >= 0", lim.name, orig)
			}
			// Halve the limit (avoid <2 since some ids cap there).
			target := max(orig/2, 1)
			prev := c.SetLimit(lim.id, target)
			if prev != orig {
				t.Errorf("SetLimit(%s, %d) returned %d, want %d", lim.name, target, prev, orig)
			}
			if cur := c.GetLimit(lim.id); cur != target {
				t.Errorf("GetLimit(%s) after set = %d, want %d", lim.name, cur, target)
			}
			// Restore.
			c.SetLimit(lim.id, orig)
		})
	}
}

// TestCoexistence_CustomNameAlongsideMattn demonstrates how to share a
// binary with mattn/go-sqlite3 by registering this driver under a separate
// name. Both drivers respond to sql.Open with their respective names; no
// conflict because we don't try to take "sqlite3" away when this pattern is
// used.
//
// The README's "Coexistence with mattn/go-sqlite3" section points readers
// at this test for a working example.
func TestCoexistence_CustomNameAlongsideMattn(t *testing.T) {
	const customName = "go-again-sqlite-coexist"

	// Pretend mattn is also linked in and has registered "sqlite3". We
	// can't actually import mattn here (would re-register and panic), but
	// the relevant property is that **a different name** routes to our
	// driver while "sqlite3" remains free to be claimed by mattn in the
	// real coexistence scenario.
	sql.Register(customName, &SQLiteDriver{})

	db, err := sql.Open(customName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var got int
	if err := db.QueryRow("SELECT 1").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Errorf("custom-named driver got %d, want 1", got)
	}

	// The default "sqlite3" name still works too — both routes hit our
	// driver in this test, but in production a sibling mattn import would
	// have grabbed "sqlite3" first and our init() would have panicked.
	// (See README for the recommended pattern: link only one side under
	// "sqlite3" or use mattn's `tag` build flag to suppress its init.)
	db2, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if err := db2.QueryRow("SELECT 2").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("default name got %d, want 2", got)
	}
}

// TestSQLite3DriverLiteral exercises the mattn idiom of registering a custom
// driver name via &sqlite3.SQLiteDriver{ConnectHook: ...}.
func TestSQLite3DriverLiteral(t *testing.T) {
	const custom = "go-again-custom-1"
	var hookFired atomic.Int32
	sql.Register(custom, &SQLiteDriver{
		ConnectHook: func(c *SQLiteConn) error {
			hookFired.Add(1)
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
	if hookFired.Load() == 0 {
		t.Errorf("ConnectHook never fired")
	}
}
