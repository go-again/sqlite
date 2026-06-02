package spellfix1_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/spellfix1"
)

func openDB(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	d := sqlite.DefaultDriver()
	prev := d.ConnectHook
	d.ConnectHook = func(c *sqlite.Conn) error {
		if prev != nil {
			if err := prev(c); err != nil {
				return err
			}
		}
		return spellfix1.Register(c)
	}
	t.Cleanup(func() { d.ConnectHook = prev })

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	return db, sc
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
	d := sqlite.DefaultDriver()
	prev := d.ConnectHook
	d.ConnectHook = func(c *sqlite.Conn) error {
		if prev != nil {
			if err := prev(c); err != nil {
				return err
			}
		}
		return spellfix1.Register(c)
	}
	t.Cleanup(func() { d.ConnectHook = prev })

	// Session 1: create, populate, close.
	{
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		sc, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
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
	{
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		sc, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer sc.Close()
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
}

var _ = errors.New
