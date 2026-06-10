package text_test

import (
	"context"
	"database/sql"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/text"
	"github.com/go-again/sqlite/internal/testhelp"
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
