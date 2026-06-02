package bloom_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/bloom"
)

// openFileSession opens a file-backed DB at path, pins one conn,
// registers bloom on it via the per-conn (*sqlite.Conn).Raw path so we
// avoid mutating Driver.ConnectHook from inside a test (the global
// mutation races with parallel tests under -race and chained hooks
// double-register the module).
func openFileSession(t *testing.T, path string) (*sql.DB, *sql.Conn) {
	t.Helper()
	if raceEnabled {
		t.Skip("skipping under -race: see openDB")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sc.Close() })

	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return bloom.Register(c)
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return db, sc
}

// openFileSessionNoCleanup mirrors openFileSession but returns close
// funcs so the persistence test can fully tear down a session before
// opening the next one (necessary because t.Cleanup runs at test end,
// LIFO, which would leave Session 1's *sql.DB still holding the file
// lock while Session 2 tries to open the same path).
func openFileSessionNoCleanup(t *testing.T, path string) (*sql.DB, *sql.Conn) {
	t.Helper()
	if raceEnabled {
		t.Skip("skipping under -race: see openDB")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return bloom.Register(c)
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return db, sc
}

func TestBloom_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bloom.db")
	ctx := context.Background()

	// Session 1: create the vtab, insert words, fully tear down before
	// opening Session 2 so the file lock is released.
	{
		db, sc := openFileSessionNoCleanup(t, path)
		if _, err := sc.ExecContext(ctx,
			`CREATE VIRTUAL TABLE persist USING bloom(size=1000, p=0.01)`); err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"durable", "persistent", "stored"} {
			if _, err := sc.ExecContext(ctx,
				`INSERT INTO persist(word) VALUES (?)`, w); err != nil {
				t.Fatal(err)
			}
		}
		sc.Close()
		db.Close()
	}

	// Session 2: reopen, look up the same words. The vtab schema is
	// rebuilt by xConnect; bits live in the shadow blob.
	{
		db, sc := openFileSessionNoCleanup(t, path)
		defer db.Close()
		defer sc.Close()
		for _, w := range []string{"durable", "persistent", "stored"} {
			rows, err := sc.QueryContext(ctx,
				`SELECT present FROM persist WHERE word = ?`, w)
			if err != nil {
				t.Fatal(err)
			}
			if !rows.Next() {
				rows.Close()
				t.Errorf("word %q: missing after reopen", w)
				continue
			}
			rows.Close()
		}
		// An unseen word should not match.
		rows, err := sc.QueryContext(ctx,
			`SELECT present FROM persist WHERE word = 'never-seen'`)
		if err != nil {
			t.Fatal(err)
		}
		if rows.Next() {
			t.Error("never-seen word matched after reopen")
		}
		rows.Close()
	}
}

func openDB(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	if raceEnabled {
		t.Skip("skipping under -race: bloom persists via (*Conn).OpenBlob; modernc Xsqlite3_blob_open trips checkptr")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return bloom.Register(c)
	}); err != nil {
		t.Fatal(err)
	}
	return db, sc
}

func TestBloom_BasicMembership(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.f USING bloom(size=1000, p=0.01)`); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"alpha", "beta", "gamma"} {
		if _, err := sc.ExecContext(ctx, `INSERT INTO temp.f(word) VALUES (?)`, w); err != nil {
			t.Fatalf("INSERT %s: %v", w, err)
		}
	}
	// Present members must report true.
	for _, w := range []string{"alpha", "beta", "gamma"} {
		var present bool
		if err := sc.QueryRowContext(ctx,
			`SELECT present FROM temp.f WHERE word = ?`, w).Scan(&present); err != nil {
			t.Fatalf("query %s: %v", w, err)
		}
		if !present {
			t.Errorf("%q should be present", w)
		}
	}
}

func TestBloom_AbsentReturnsNoRow(t *testing.T) {
	// A miss should produce no rows, NOT a row with present=false.
	// (Cursor.Eof returns true when bloom says "definitely not".)
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.f USING bloom(size=100, p=0.001)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO temp.f(word) VALUES ('only')`); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(ctx,
		`SELECT present FROM temp.f WHERE word = 'nope'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("expected no rows for absent member")
	}
}

func TestBloom_FalsePositiveRateBounded(t *testing.T) {
	// Build a filter sized for 1000 entries @ p=0.01, then probe with
	// 10000 unseen keys. Expected FP rate ≤ ~3% (the math gives ~1%;
	// allow slack for finite-sample variance and the Kirsch-Mitzenmacher
	// double-hash approximation).
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.f USING bloom(size=1000, p=0.01)`); err != nil {
		t.Fatal(err)
	}
	for i := range 1000 {
		if _, err := sc.ExecContext(ctx, `INSERT INTO temp.f(word) VALUES (?)`,
			fmt.Sprintf("present-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	fp := 0
	for i := range 10000 {
		var present sql.NullBool
		_ = sc.QueryRowContext(ctx, `SELECT present FROM temp.f WHERE word = ?`,
			fmt.Sprintf("absent-%d", i)).Scan(&present)
		if present.Valid && present.Bool {
			fp++
		}
	}
	rate := float64(fp) / 10000
	if rate > 0.03 {
		t.Errorf("false-positive rate %.4f exceeds 3%% bound", rate)
	}
}

func TestBloom_DeleteUpdateRejected(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx,
		`CREATE VIRTUAL TABLE temp.f USING bloom(size=10)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO temp.f(word) VALUES ('a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `DELETE FROM temp.f WHERE word = 'a'`); err == nil ||
		!strings.Contains(err.Error(), "deleted") {
		t.Errorf("DELETE should fail, got %v", err)
	}
	if _, err := sc.ExecContext(ctx,
		`UPDATE temp.f SET present = 0 WHERE word = 'a'`); err == nil ||
		!strings.Contains(err.Error(), "updated") {
		t.Errorf("UPDATE should fail, got %v", err)
	}
}

func TestBloom_BadArgs(t *testing.T) {
	_, sc := openDB(t)
	cases := []string{
		`CREATE VIRTUAL TABLE temp.t USING bloom(size=-1)`,
		`CREATE VIRTUAL TABLE temp.t USING bloom(p=0)`,
		`CREATE VIRTUAL TABLE temp.t USING bloom(p=1.5)`,
		`CREATE VIRTUAL TABLE temp.t USING bloom(k=0)`,
		`CREATE VIRTUAL TABLE temp.t USING bloom(unknown=1)`,
	}
	for _, q := range cases {
		if _, err := sc.ExecContext(context.Background(), q); err == nil {
			t.Errorf("%q: expected error, got nil", q)
		}
	}
}

func TestBloom_ModuleName(t *testing.T) {
	if bloom.ModuleName != "bloom" {
		t.Errorf("ModuleName=%q, want %q", bloom.ModuleName, "bloom")
	}
}
