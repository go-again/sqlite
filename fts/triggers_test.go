package fts_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/go-again/sqlite/fts"
)

// setupExternalContent creates a source table and an external-content
// FTS5 table with the requested SyncMode. Returns the configured
// fts.Index, the db handle (for direct INSERT/UPDATE/DELETE on the
// source), and the source table name.
func setupExternalContent(t *testing.T, mode fts.SyncMode) (*fts.Index[int64, string], *sql.DB, string) {
	t.Helper()
	db := openDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE docs (
		id   INTEGER PRIMARY KEY,
		body TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	idx, err := fts.New[int64, string](ctx, db, "docs_fts", fts.Options{
		Columns:   []string{"body"},
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
		External: &fts.External{
			ContentTable: "docs",
			ContentRowid: "id",
			SyncTriggers: mode,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return idx, db, "docs"
}

// TestExternal_SyncTriggers_Insert exercises pantry's headline case:
// SyncInsert plus a raw INSERT on the source table makes the row
// discoverable via the typed Search API.
func TestExternal_SyncTriggers_Insert(t *testing.T) {
	idx, db, source := setupExternalContent(t, fts.SyncAll)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO `+source+` (body) VALUES ('the quick brown fox')`); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.SearchSlice(ctx, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("after INSERT: hits=%d, want 1", len(hits))
	}
}

// TestExternal_SyncTriggers_Update verifies AFTER UPDATE fires both
// the 'delete' magic-row and the re-insert, so the old text is gone
// and the new text is searchable.
func TestExternal_SyncTriggers_Update(t *testing.T) {
	idx, db, source := setupExternalContent(t, fts.SyncAll)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO `+source+` (body) VALUES ('one old text')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE `+source+` SET body = 'replaced fox content' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	gone, err := idx.SearchSlice(ctx, fts.Term("old"))
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Errorf("after UPDATE: 'old' hits=%d, want 0", len(gone))
	}
	hits, err := idx.SearchSlice(ctx, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("after UPDATE: 'fox' hits=%d, want 1", len(hits))
	}
}

// TestExternal_SyncTriggers_Delete verifies AFTER DELETE removes the
// row from the FTS5 index.
func TestExternal_SyncTriggers_Delete(t *testing.T) {
	idx, db, source := setupExternalContent(t, fts.SyncAll)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO `+source+` (body) VALUES ('ephemeral fox')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM `+source+` WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	hits, err := idx.SearchSlice(ctx, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("after DELETE: hits=%d, want 0", len(hits))
	}
}

// TestExternal_SyncTriggers_PartialMode confirms the bitmask is honored
// individually: SyncInsert alone installs only the AFTER INSERT trigger,
// so subsequent UPDATEs do NOT re-index.
func TestExternal_SyncTriggers_PartialMode(t *testing.T) {
	idx, db, source := setupExternalContent(t, fts.SyncInsert)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO `+source+` (body) VALUES ('seed fox')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE `+source+` SET body = 'now replaced' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	stale, err := idx.SearchSlice(ctx, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 {
		t.Errorf("partial mode: 'fox' hits=%d, want 1 (stale)", len(stale))
	}
}

// TestExternal_SyncTriggers_Idempotent confirms re-running New with
// WithIfNotExists + SyncTriggers doesn't error on the second pass.
// Trigger CREATE uses IF NOT EXISTS so the install is safe to retry.
func TestExternal_SyncTriggers_Idempotent(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE docs (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	opts := fts.Options{
		Columns:   []string{"body"},
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
		External: &fts.External{
			ContentTable: "docs",
			ContentRowid: "id",
			SyncTriggers: fts.SyncAll,
		},
	}
	if _, err := fts.New[int64, string](ctx, db, "docs_fts", opts); err != nil {
		t.Fatalf("first New: %v", err)
	}
	if _, err := fts.New[int64, string](ctx, db, "docs_fts", opts, fts.WithIfNotExists()); err != nil {
		t.Errorf("second New (idempotent): %v", err)
	}
}
