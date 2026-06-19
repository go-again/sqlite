package fts_test

import (
	"context"
	"strings"
	"testing"

	"gosqlite.org/fts"
)

// TestTokenizer_NoInjectionViaTokenchars is the end-to-end proof for sweep #3
// F1: a single quote in a free-form tokenizer field used to break out of the
// tokenize='…' literal and could corrupt / inject into the CREATE VIRTUAL
// TABLE. After the fix the apostrophe is doubled, so a payload crafted to drop
// a table is inert — it stays inside one SQL string literal (FTS5 then rejects
// it as a contained tokenize parse error), and the target table survives.
func TestTokenizer_NoInjectionViaTokenchars(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE victim(x)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO victim VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	// Crafted to terminate the literal and run a DROP. Post-fix New returns a
	// contained error (or succeeds); either way the injection must not execute.
	_, _ = fts.New[int64, string](ctx, db, "docs", fts.Options{
		Tokenizer: fts.Unicode61{Tokenchars: "'); DROP TABLE victim; --"},
	})
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM victim`).Scan(&n); err != nil {
		t.Fatalf("victim table should still exist (no injection): %v", err)
	}
	if n != 1 {
		t.Errorf("victim row count = %d, want 1 (the injected DROP must not have run)", n)
	}
}

// TestTokenizer_NULRejected confirms a NUL in a tokenizer arg is refused with a
// clear error rather than silently truncating the generated SQL.
func TestTokenizer_NULRejected(t *testing.T) {
	db := openDB(t)
	_, err := fts.New[int64, string](context.Background(), db, "docs", fts.Options{
		Tokenizer: fts.Unicode61{Tokenchars: "a\x00b"},
	})
	if err == nil {
		t.Fatal("New with a NUL in Tokenchars should error")
	}
	if !strings.Contains(err.Error(), "NUL") {
		t.Errorf("error = %v, want it to mention the NUL byte", err)
	}
}
