package sqlite

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
)

func collationNeededLen() int {
	collationNeeded.mu.RLock()
	defer collationNeeded.mu.RUnlock()
	return len(collationNeeded.m)
}

func seedColl(t *testing.T, ctx context.Context, sc *sql.Conn) {
	t.Helper()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"b", "a", "c"} {
		if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (?)`, v); err != nil {
			t.Fatal(err)
		}
	}
}

func collOrder(t *testing.T, ctx context.Context, sc *sql.Conn, collation string) []string {
	t.Helper()
	rows, err := sc.QueryContext(ctx, `SELECT x FROM t ORDER BY x COLLATE `+collation)
	if err != nil {
		t.Fatalf("ordered query (COLLATE %s): %v", collation, err)
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
	return got
}

// TestCollationNeeded_AnyFakesBinary: an unknown collation errors until
// AnyCollationNeeded defines it on demand as byte-wise order.
func TestCollationNeeded_AnyFakesBinary(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	seedColl(t, ctx, sc)

	if _, err := sc.QueryContext(ctx, `SELECT x FROM t ORDER BY x COLLATE weird_locale`); err == nil {
		t.Fatal("expected an error referencing an unknown collation, got nil")
	}

	if err := c.AnyCollationNeeded(); err != nil {
		t.Fatalf("AnyCollationNeeded: %v", err)
	}
	got := collOrder(t, ctx, sc, "weird_locale")
	if want := []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("order under faked collation = %v, want %v (byte-wise)", got, want)
	}
}

// TestCollationNeeded_Custom: the callback may install a real comparator, which
// then drives ordering (here a reverse collation).
func TestCollationNeeded_Custom(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if err := c.CollationNeeded(func(conn *Conn, name string) {
		if name == "rev" {
			_ = conn.RegisterCollation("rev", func(a, b string) int { return strings.Compare(b, a) })
		}
	}); err != nil {
		t.Fatalf("CollationNeeded: %v", err)
	}
	seedColl(t, ctx, sc)

	got := collOrder(t, ctx, sc, "rev")
	if want := []string{"c", "b", "a"}; !slices.Equal(got, want) {
		t.Errorf("order under reverse collation = %v, want %v", got, want)
	}
}

// TestCollationNeeded_DrainOnClose: the minted registry id is reclaimed when the
// connection closes, so per-conn registration does not leak.
func TestCollationNeeded_DrainOnClose(t *testing.T) {
	base := collationNeededLen()

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
	if err := sc.Raw(func(dc any) error {
		return dc.(*Conn).CollationNeeded(func(*Conn, string) {})
	}); err != nil {
		t.Fatalf("install handler: %v", err)
	}
	if got := collationNeededLen(); got != base+1 {
		t.Fatalf("after register: registry len = %d, want %d", got, base+1)
	}
	_ = sc.Close()
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := collationNeededLen(); got != base {
		t.Errorf("registry not drained on close: have %d, want %d", got, base)
	}
}
