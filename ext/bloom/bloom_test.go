package bloom_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/bloom"
)

func openDB(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
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
