package spellfix1_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/go-again/sqlite/ext/spellfix1"
	"github.com/go-again/sqlite/internal/testhelp"
)

func openDB(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	testhelp.WithConnectHook(t, spellfix1.Register)
	return testhelp.OpenPinned(t, "sqlite", ":memory:")
}

func TestSpellfix1_BasicLookup(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE vocab USING spellfix1`); err != nil {
		t.Fatal(err)
	}
	words := []string{"apple", "banana", "cherry", "apricot", "grape"}
	for _, w := range words {
		if _, err := sc.ExecContext(ctx,
			`INSERT INTO vocab(word) VALUES (?)`, w); err != nil {
			t.Fatal(err)
		}
	}

	// Look up "aple" — should match "apple" with distance 1.
	rows, err := sc.QueryContext(ctx,
		`SELECT word, distance FROM vocab WHERE word MATCH 'aple' LIMIT 3`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var bestWord string
	var bestDist int
	if rows.Next() {
		if err := rows.Scan(&bestWord, &bestDist); err != nil {
			t.Fatal(err)
		}
	}
	if bestWord != "apple" {
		t.Errorf("top match=%q, want \"apple\"", bestWord)
	}
	if bestDist > 1 {
		t.Errorf("distance=%d, want <= 1", bestDist)
	}
}

func TestSpellfix1_RankBoost(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE v USING spellfix1`); err != nil {
		t.Fatal(err)
	}
	// "color" and "colour" both within distance 1 of "colour"; rank
	// boost should prefer the higher-ranked one.
	if _, err := sc.ExecContext(ctx, `INSERT INTO v(word, rank) VALUES ('color', 10)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO v(word, rank) VALUES ('colour', 100)`); err != nil {
		t.Fatal(err)
	}
	var top string
	if err := sc.QueryRowContext(ctx,
		`SELECT word FROM v WHERE word MATCH 'colur' LIMIT 1`).Scan(&top); err != nil {
		t.Fatal(err)
	}
	if top != "colour" {
		t.Errorf("top=%q, want \"colour\" (higher rank should win the tie)", top)
	}
}

func TestSpellfix1_ScopeBound(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE v USING spellfix1`); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"hello", "world", "xyzzy"} {
		if _, err := sc.ExecContext(ctx, `INSERT INTO v(word) VALUES (?)`, w); err != nil {
			t.Fatal(err)
		}
	}
	// scope=1 should cap edit-distance to 1, eliminating xyzzy from a
	// "hello" query (distance > 1).
	rows, err := sc.QueryContext(ctx,
		`SELECT word FROM v WHERE word MATCH 'helo' AND scope = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var w string
		_ = rows.Scan(&w)
		if w == "xyzzy" {
			t.Errorf("xyzzy leaked past scope=1")
		}
	}
}

func TestSpellfix1_RequiresMatch(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE v USING spellfix1`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.QueryContext(ctx, `SELECT * FROM v`); err == nil {
		t.Error("missing MATCH: want error, got nil")
	}
}

func TestSpellfix1_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spell.db")
	ctx := context.Background()
	testhelp.WithConnectHook(t, spellfix1.Register)

	// Session 1: create, populate, close.
	{
		db, sc := testhelp.OpenPinned(t, "sqlite", path)
		if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE persist USING spellfix1`); err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"durable", "stored", "persistent"} {
			if _, err := sc.ExecContext(ctx, `INSERT INTO persist(word) VALUES (?)`, w); err != nil {
				t.Fatal(err)
			}
		}
		sc.Close()
		db.Close()
	}

	// Session 2: reopen, look up "durabl" should match "durable" with
	// distance 1.
	_, sc := testhelp.OpenPinned(t, "sqlite", path)
	var w string
	var d int
	if err := sc.QueryRowContext(ctx,
		`SELECT word, distance FROM persist WHERE word MATCH 'durabl' LIMIT 1`).Scan(&w, &d); err != nil {
		t.Fatalf("after reopen: %v", err)
	}
	if w != "durable" {
		t.Errorf("after reopen: top=%q, want \"durable\"", w)
	}
	if d > 1 {
		t.Errorf("after reopen: distance=%d, want <= 1", d)
	}
}
