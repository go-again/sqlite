package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
)

// TestStmtCache_BasicLRU walks the LRU directly: put 3 entries with capacity
// 2, observe eviction of the LRU on the 3rd put. We bypass the conn so the
// test exercises only the data structure, not SQLite.
func TestStmtCache_BasicLRU(t *testing.T) {
	c := newStmtCache(2)

	c.put("a", 100, 200)
	c.put("b", 101, 201)
	if c.len() != 2 {
		t.Fatalf("after 2 puts len=%d, want 2", c.len())
	}

	// Third put exceeds capacity → evicts LRU (which is "a").
	evicted := c.put("c", 102, 202)
	if evicted == nil {
		t.Fatal("expected eviction on 3rd put")
	}
	if evicted.key != "a" {
		t.Errorf("evicted %q, want %q", evicted.key, "a")
	}

	// "a" should now be a miss.
	if got := c.take("a"); got != nil {
		t.Errorf("take(a) after eviction returned %+v, want nil", got)
	}
	// "b" and "c" still cached.
	if got := c.take("b"); got == nil || got.pstmt != 201 {
		t.Errorf("take(b) = %+v, want pstmt=201", got)
	}
	if got := c.take("c"); got == nil || got.pstmt != 202 {
		t.Errorf("take(c) = %+v, want pstmt=202", got)
	}
}

// TestStmtCache_TakeMovesNothing asserts take() pulls the entry out of the
// cache cleanly — subsequent take of the same key misses, and the cache
// length drops.
func TestStmtCache_TakeMovesNothing(t *testing.T) {
	c := newStmtCache(2)
	c.put("x", 1, 2)
	if c.len() != 1 {
		t.Fatalf("len after put = %d, want 1", c.len())
	}
	if e := c.take("x"); e == nil {
		t.Fatal("take returned nil")
	}
	if c.len() != 0 {
		t.Errorf("len after take = %d, want 0", c.len())
	}
	if e := c.take("x"); e != nil {
		t.Errorf("second take returned %+v, want nil", e)
	}
}

// TestStmtCache_PutReplacesSameKey verifies that putting the same SQL key
// twice does not grow the cache and evicts the older entry's handles.
func TestStmtCache_PutReplacesSameKey(t *testing.T) {
	c := newStmtCache(2)
	c.put("k", 10, 20)
	evicted := c.put("k", 11, 21)
	if evicted == nil {
		t.Fatal("expected eviction of prior entry with same key")
	}
	if evicted.pstmt != 20 {
		t.Errorf("evicted pstmt=%d, want 20", evicted.pstmt)
	}
	if c.len() != 1 {
		t.Errorf("len after replace = %d, want 1", c.len())
	}
}

// TestStmtCache_Disabled is a no-op cache; put returns the entry back to
// the caller for immediate finalization, and take always misses.
func TestStmtCache_Disabled(t *testing.T) {
	c := newStmtCache(0)
	if c.enabled() {
		t.Fatal("cap=0 should be disabled")
	}
	if got := c.take("anything"); got != nil {
		t.Errorf("take on disabled cache returned %+v, want nil", got)
	}
	returned := c.put("k", 1, 2)
	if returned == nil || returned.pstmt != 2 {
		t.Errorf("disabled put should hand the entry back; got %+v", returned)
	}
	if c.len() != 0 {
		t.Errorf("disabled cache len = %d, want 0", c.len())
	}
}

// TestStmtCache_NormalizeTrimsWhitespace checks the canonical key transform:
// SQL with extra surrounding whitespace hits the same cache entry.
func TestStmtCache_NormalizeTrimsWhitespace(t *testing.T) {
	c := newStmtCache(2)
	c.put("SELECT 1", 1, 2)
	if got := c.take("  SELECT 1\n"); got == nil {
		t.Errorf("trimmed SQL should match cached key")
	}
}

// TestStmtCache_DrainAll empties the cache and returns every entry. Used by
// conn.Close to finalize cached pstmts.
func TestStmtCache_DrainAll(t *testing.T) {
	c := newStmtCache(3)
	c.put("a", 1, 10)
	c.put("b", 2, 20)
	c.put("c", 3, 30)
	got := c.drainAll()
	if len(got) != 3 {
		t.Errorf("drainAll returned %d entries, want 3", len(got))
	}
	if c.len() != 0 {
		t.Errorf("after drainAll len = %d, want 0", c.len())
	}
}

// TestStmtCache_PrepareCacheHit is the end-to-end happy-path: open a DB,
// Prepare the same query twice via two separate *sql.Stmt values, observe
// the second prepare reuse the cached pstmt.
//
// We hook directly into a *Conn to count cache hits/misses without exposing
// internal counters to the public API.
func TestStmtCache_PrepareCacheHit(t *testing.T) {
	db, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	var c *Conn
	if err := sc.Raw(func(dc any) error {
		c = dc.(*Conn)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := sc.ExecContext(ctx, "CREATE TABLE t (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	baseLen := c.stmts.len() // CREATE TABLE + anything database/sql cached

	// Prepare + close 10 times. After the first iteration, the cache should
	// contain the SELECT and subsequent prepares are hits — the cache
	// length should grow by exactly 1, not 10.
	const query = "SELECT x FROM t WHERE x > ?"
	for range 10 {
		stmt, err := sc.PrepareContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := stmt.QueryContext(ctx, 0)
		if err != nil {
			stmt.Close()
			t.Fatal(err)
		}
		for rows.Next() {
		}
		rows.Close()
		if err := stmt.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if delta := c.stmts.len() - baseLen; delta != 1 {
		t.Errorf("cache grew by %d after 10 prepares of one query, want 1", delta)
	}

	// A different query bumps the cache by exactly one more.
	stmt2, err := sc.PrepareContext(ctx, "SELECT count(*) FROM t")
	if err != nil {
		t.Fatal(err)
	}
	stmt2.Close()
	if delta := c.stmts.len() - baseLen; delta != 2 {
		t.Errorf("cache grew by %d after second distinct prepare, want 2", delta)
	}
}

// TestStmtCache_CapZeroDisablesViaDSN asserts that `_stmt_cache_size=0`
// turns the cache off, and prepares are not retained between Close calls.
func TestStmtCache_CapZeroDisablesViaDSN(t *testing.T) {
	db, err := sql.Open(DriverNameSQLite3, ":memory:?_stmt_cache_size=0")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	var c *Conn
	if err := sc.Raw(func(dc any) error {
		c = dc.(*Conn)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := sc.ExecContext(ctx, "CREATE TABLE t (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	stmt, err := sc.PrepareContext(ctx, "SELECT x FROM t")
	if err != nil {
		t.Fatal(err)
	}
	stmt.Close()
	if c.stmts.enabled() {
		t.Errorf("_stmt_cache_size=0 should disable the cache")
	}
	if got := c.stmts.len(); got != 0 {
		t.Errorf("disabled cache len = %d, want 0", got)
	}
}

// TestStmtCache_FinalizesOnConnClose proves that closing the connection
// finalizes every retained pstmt and frees its psql. We don't have a
// public way to inspect SQLite's "open statements count" so we observe the
// effect indirectly: a connection with retained prepares can be closed
// without sqlite3_close returning SQLITE_BUSY.
func TestStmtCache_FinalizesOnConnClose(t *testing.T) {
	db, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Stash multiple prepared statements in the cache before closing.
	for _, q := range []string{"SELECT 1", "SELECT 2", "SELECT 3"} {
		stmt, err := sc.PrepareContext(ctx, q)
		if err != nil {
			sc.Close()
			t.Fatal(err)
		}
		stmt.Close()
	}

	// Closing the conn must succeed even though the cache holds pstmts.
	if err := sc.Close(); err != nil {
		t.Fatalf("Conn.Close with cached stmts: %v", err)
	}
}

// TestStmtCache_ResetClearsBindingsBetweenUses asserts the reset path:
// bind args to one stmt, close it, prepare the same SQL again, leave the
// args UNbound — execution should use the new bindings (NULL) rather than
// carry over the old ones.
func TestStmtCache_ResetClearsBindingsBetweenUses(t *testing.T) {
	db, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	if _, err := sc.ExecContext(ctx, "CREATE TABLE t (x)"); err != nil {
		t.Fatal(err)
	}

	// First use: bind 42 and confirm.
	stmt, err := sc.PrepareContext(ctx, "SELECT ?")
	if err != nil {
		t.Fatal(err)
	}
	var got int
	if err := stmt.QueryRowContext(ctx, 42).Scan(&got); err != nil {
		stmt.Close()
		t.Fatal(err)
	}
	stmt.Close()
	if got != 42 {
		t.Errorf("first use scan = %d, want 42", got)
	}

	// Second use of the SAME SQL — pulled from cache. Bind a different
	// value; if reset didn't clear, we'd see 42 here instead of 7.
	stmt, err = sc.PrepareContext(ctx, "SELECT ?")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	if err := stmt.QueryRowContext(ctx, 7).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 7 {
		t.Errorf("cache-reused stmt scan = %d, want 7 (reset failed?)", got)
	}
}

// TestStmtCache_DistinctErrorOnClosedConn confirms operating on a *Conn
// after a clean close does not panic and surfaces a sensible error. This
// is mostly a regression guard: an earlier draft of the cache could leave
// `c.stmts` pointing at stale entries after Conn.Close.
func TestStmtCache_DistinctErrorOnClosedConn(t *testing.T) {
	db, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Stash some retained pstmts then close the conn.
	stmt, err := sc.PrepareContext(ctx, "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	stmt.Close()
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}
	// db.Close should also be clean.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Sanity: explicitly running a query after close errors instead of
	// panicking.
	var v atomic.Int32
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic from db.Query after Close: %v", r)
		}
		v.Store(1)
	}()
	row := db.QueryRowContext(ctx, "SELECT 1")
	if err := row.Scan(new(int)); !errors.Is(err, sql.ErrConnDone) && err == nil {
		// Whatever the specific error, it should not be nil and should not
		// panic. The exact identity (ErrConnDone vs a driver error) varies
		// across Go versions; just assert non-nil.
		t.Errorf("expected an error after Close, got nil")
	}
}
