package spellfix1_test

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/spellfix1"
	"github.com/go-again/sqlite/internal/testhelp"
)

// openVocabDB returns a *sql.DB with the spellfix1 module installed on
// every connection (via ConnectHook) and pinned to a single conn so the
// in-memory vtab persists across calls. The typed Vocab API runs over the
// *sql.DB directly, so the module must be pool-wide, not on one conn.
func openVocabDB(t *testing.T) *sql.DB {
	t.Helper()
	testhelp.WithConnectHook(t, spellfix1.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestVocab_CreateAddCorrect(t *testing.T) {
	ctx := context.Background()
	db := openVocabDB(t)

	v, err := spellfix1.Create(ctx, db, "items_vocab")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := v.AddMany(ctx, []string{"apple", "banana", "cherry"}); err != nil {
		t.Fatalf("AddMany: %v", err)
	}

	matches, err := v.Correct(ctx, "aple", spellfix1.WithMaxDistance(2), spellfix1.WithLimit(3))
	if err != nil {
		t.Fatalf("Correct: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one match for 'aple'")
	}
	if matches[0].Word != "apple" {
		t.Errorf("best match = %q, want %q", matches[0].Word, "apple")
	}
	if matches[0].Distance < 1 {
		t.Errorf("distance = %d, want >= 1", matches[0].Distance)
	}
}

func TestVocab_Add_Dedupe(t *testing.T) {
	ctx := context.Background()
	db := openVocabDB(t)
	v, err := spellfix1.Create(ctx, db, "vocab")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for range 3 {
		if err := v.Add(ctx, "apple"); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	n, err := v.Size(ctx)
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if n != 1 {
		t.Errorf("Size after adding 'apple' 3x = %d, want 1 (deduped)", n)
	}
}

func TestVocab_Size_UsesShadowTable(t *testing.T) {
	ctx := context.Background()
	db := openVocabDB(t)
	v, err := spellfix1.Create(ctx, db, "vocab")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := v.AddMany(ctx, []string{"a", "b", "c"}); err != nil {
		t.Fatalf("AddMany: %v", err)
	}
	// Size must not trip the vtab's WHERE-word-MATCH-required constraint:
	// it counts the shadow storage table directly.
	n, err := v.Size(ctx)
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if n != 3 {
		t.Errorf("Size = %d, want 3", n)
	}
}

func TestVocab_Create_WithIfNotExists_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := openVocabDB(t)
	if _, err := spellfix1.Create(ctx, db, "vocab", spellfix1.WithIfNotExists()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := spellfix1.Create(ctx, db, "vocab", spellfix1.WithIfNotExists()); err != nil {
		t.Fatalf("second Create with WithIfNotExists must be a no-op, got: %v", err)
	}
}

func TestVocab_Create_ErrAlreadyExists(t *testing.T) {
	ctx := context.Background()
	db := openVocabDB(t)
	if _, err := spellfix1.Create(ctx, db, "vocab"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := spellfix1.Create(ctx, db, "vocab")
	if !errors.Is(err, spellfix1.ErrAlreadyExists) {
		t.Errorf("second Create error = %v, want errors.Is(err, ErrAlreadyExists)", err)
	}
}

func TestVocab_AddMany_OneTransaction(t *testing.T) {
	// Mirrors vec's TestTyped_BatchInsert_OneTx: install a commit hook on
	// the single pooled conn before any vtab work, then assert AddMany
	// commits exactly once for the whole batch (not once per word).
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	var commits int32
	if err := sc.Raw(func(dc any) error {
		c, ok := dc.(*sqlite.Conn)
		if !ok {
			return errors.New("driver conn is not *sqlite.Conn")
		}
		if err := spellfix1.Register(c); err != nil {
			return err
		}
		c.RegisterCommitHook(func() int32 { atomic.AddInt32(&commits, 1); return 0 })
		return nil
	}); err != nil {
		_ = sc.Close()
		t.Fatalf("install module + commit hook: %v", err)
	}
	// Release the pinned conn; MaxOpenConns=1 hands the same physical conn
	// (module + hook intact) back to db.ExecContext below.
	if err := sc.Close(); err != nil {
		t.Fatalf("sc.Close: %v", err)
	}

	v, err := spellfix1.Create(ctx, db, "vocab")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	baseline := atomic.LoadInt32(&commits) // CREATE VIRTUAL TABLE autocommits
	if err := v.AddMany(ctx, []string{"apple", "banana", "cherry", "date", "elderberry"}); err != nil {
		t.Fatalf("AddMany: %v", err)
	}
	if got := atomic.LoadInt32(&commits) - baseline; got != 1 {
		t.Errorf("AddMany fired %d commits, want 1", got)
	}
}

func TestVocab_CorrectSQL_ReturnsBindArgs(t *testing.T) {
	ctx := context.Background()
	db := openVocabDB(t)
	v, err := spellfix1.Create(ctx, db, "vocab")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := v.AddMany(ctx, []string{"apple", "apricot"}); err != nil {
		t.Fatalf("AddMany: %v", err)
	}

	q, args, err := v.CorrectSQL("aple", spellfix1.WithMaxDistance(3), spellfix1.WithLimit(2))
	if err != nil {
		t.Fatalf("CorrectSQL: %v", err)
	}
	if len(args) != 3 {
		t.Fatalf("args = %v, want 3 (term, scope, limit)", args)
	}
	if args[0] != "aple" || args[1] != 3 || args[2] != 2 {
		t.Errorf("args = %v, want [aple 3 2]", args)
	}
	// The emitted SQL + args must round-trip through database/sql.
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		t.Fatalf("QueryContext(%q): %v", q, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Errorf("expected at least one row from CorrectSQL output")
	}
}

func TestVocab_Drop(t *testing.T) {
	ctx := context.Background()
	db := openVocabDB(t)
	v, err := spellfix1.Create(ctx, db, "vocab")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := v.Add(ctx, "apple"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := v.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	// After Drop the vtab (and its shadow storage) is gone, so a fresh
	// Create with the same name succeeds.
	if _, err := spellfix1.Create(ctx, db, "vocab"); err != nil {
		t.Errorf("Create after Drop: %v", err)
	}
}
