package text_test

import (
	"context"
	"database/sql"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/ext/text"
	"gosqlite.org/internal/testhelp"
)

func openDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	testhelp.WithConnectHook(t, text.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return context.Background(), db
}

func str(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var s string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return s
}

func TestText_Reverse(t *testing.T) {
	ctx, db := openDB(t)
	if got := str(t, ctx, db, `SELECT text_reverse('abc')`); got != "cba" {
		t.Errorf("text_reverse('abc') = %q, want cba", got)
	}
	// Rune-aware: multibyte runes are not byte-reversed into mojibake.
	if got := str(t, ctx, db, `SELECT text_reverse('abç')`); got != "çba" {
		t.Errorf("text_reverse('abç') = %q, want çba", got)
	}
}

func TestText_Repeat(t *testing.T) {
	ctx, db := openDB(t)
	if got := str(t, ctx, db, `SELECT text_repeat('ab', 3)`); got != "ababab" {
		t.Errorf("text_repeat('ab',3) = %q, want ababab", got)
	}
	if got := str(t, ctx, db, `SELECT text_repeat('x', 0)`); got != "" {
		t.Errorf("text_repeat('x',0) = %q, want empty", got)
	}
}

func TestText_Pad(t *testing.T) {
	ctx, db := openDB(t)
	if got := str(t, ctx, db, `SELECT text_lpad('x', 3)`); got != "  x" {
		t.Errorf("text_lpad('x',3) = %q, want '  x'", got)
	}
	if got := str(t, ctx, db, `SELECT text_lpad('x', 3, '-')`); got != "--x" {
		t.Errorf("text_lpad('x',3,'-') = %q, want '--x'", got)
	}
	if got := str(t, ctx, db, `SELECT text_rpad('x', 3, '-')`); got != "x--" {
		t.Errorf("text_rpad('x',3,'-') = %q, want 'x--'", got)
	}
	// Already long enough → unchanged.
	if got := str(t, ctx, db, `SELECT text_rpad('hello', 3)`); got != "hello" {
		t.Errorf("text_rpad('hello',3) = %q, want hello", got)
	}
}

func TestText_OverflowGuards(t *testing.T) {
	ctx, db := openDB(t)
	// A hostile count must error, not OOM or panic. With a multi-rune pad and a
	// near-MaxInt64 count, the size arithmetic would overflow int64 and bypass
	// a naive guard — these assert the overflow-safe guard trips instead.
	var s string
	if err := db.QueryRowContext(ctx,
		`SELECT text_lpad('x', 9223372036854775807, '--')`).Scan(&s); err == nil {
		t.Error("text_lpad with a huge count should error (overflow guard)")
	}
	if err := db.QueryRowContext(ctx,
		`SELECT text_rpad('x', 9223372036854775807)`).Scan(&s); err == nil {
		t.Error("text_rpad with a huge count should error")
	}
	if err := db.QueryRowContext(ctx,
		`SELECT text_repeat('hello world this is a longish string', 9223372036854775807)`).Scan(&s); err == nil {
		t.Error("text_repeat with a huge count should error (overflow guard)")
	}
	// A count just under the cap still succeeds (boundary not over-tightened).
	if got := str(t, ctx, db, `SELECT text_repeat('ab', 1000)`); len(got) != 2000 {
		t.Errorf("text_repeat('ab',1000) len = %d, want 2000", len(got))
	}
	// The pad path has its own (different) guard formula; a wide-but-legal pad
	// must succeed and produce exactly the requested width, proving that guard
	// is not over-tightened either.
	if got := str(t, ctx, db, `SELECT text_lpad('x', 5000, 'ab')`); len(got) != 5000 {
		t.Errorf("text_lpad('x',5000,'ab') len = %d, want 5000", len(got))
	} else if got[len(got)-1] != 'x' {
		t.Errorf("text_lpad result should end with the original 'x', ends %q", got[len(got)-4:])
	}
	if got := str(t, ctx, db, `SELECT text_rpad('x', 5000, 'ab')`); len(got) != 5000 {
		t.Errorf("text_rpad('x',5000,'ab') len = %d, want 5000", len(got))
	} else if got[0] != 'x' {
		t.Errorf("text_rpad result should start with the original 'x', starts %q", got[:4])
	}
}

func TestText_Split(t *testing.T) {
	ctx, db := openDB(t)
	if got := str(t, ctx, db, `SELECT text_split('a,b,c', ',', 2)`); got != "b" {
		t.Errorf("text_split('a,b,c',',',2) = %q, want b", got)
	}
	if got := str(t, ctx, db, `SELECT text_split('a,b,c', ',', 9)`); got != "" {
		t.Errorf("text_split out of range = %q, want empty", got)
	}
}

var _ func(*sqlite.Conn) error = text.Register
