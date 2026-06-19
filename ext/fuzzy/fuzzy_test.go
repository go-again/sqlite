package fuzzy_test

import (
	"context"
	"database/sql"
	"math"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/ext/fuzzy"
	"gosqlite.org/internal/testhelp"
)

func openDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	testhelp.WithConnectHook(t, fuzzy.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return context.Background(), db
}

func scanInt(t *testing.T, ctx context.Context, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}

func scanFloat(t *testing.T, ctx context.Context, db *sql.DB, q string, args ...any) float64 {
	t.Helper()
	var f float64
	if err := db.QueryRowContext(ctx, q, args...).Scan(&f); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return f
}

func scanStr(t *testing.T, ctx context.Context, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var s string
	if err := db.QueryRowContext(ctx, q, args...).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return s
}

func TestFuzzy_EditDistances(t *testing.T) {
	ctx, db := openDB(t)
	// Canonical Levenshtein example.
	if got := scanInt(t, ctx, db, `SELECT levenshtein('kitten', 'sitting')`); got != 3 {
		t.Errorf("levenshtein(kitten,sitting) = %d, want 3", got)
	}
	if got := scanInt(t, ctx, db, `SELECT levenshtein('', 'abc')`); got != 3 {
		t.Errorf("levenshtein('',abc) = %d, want 3", got)
	}
	if got := scanInt(t, ctx, db, `SELECT levenshtein('abc', 'abc')`); got != 0 {
		t.Errorf("levenshtein(abc,abc) = %d, want 0", got)
	}
	// A transposition costs 2 under plain Levenshtein, 1 under Damerau.
	if got := scanInt(t, ctx, db, `SELECT levenshtein('ca', 'ac')`); got != 2 {
		t.Errorf("levenshtein(ca,ac) = %d, want 2", got)
	}
	if got := scanInt(t, ctx, db, `SELECT damerau_levenshtein('ca', 'ac')`); got != 1 {
		t.Errorf("damerau_levenshtein(ca,ac) = %d, want 1 (adjacent transposition)", got)
	}
	// Unicode-aware: each rune counts once.
	if got := scanInt(t, ctx, db, `SELECT levenshtein('café', 'cafe')`); got != 1 {
		t.Errorf("levenshtein(café,cafe) = %d, want 1", got)
	}
}

func TestFuzzy_Hamming(t *testing.T) {
	ctx, db := openDB(t)
	if got := scanInt(t, ctx, db, `SELECT hamming('karolin', 'kathrin')`); got != 3 {
		t.Errorf("hamming(karolin,kathrin) = %d, want 3", got)
	}
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT hamming('ab', 'abc')`).Scan(&n); err == nil {
		t.Error("hamming on unequal-length strings should error")
	}
}

func TestFuzzy_JaroWinkler(t *testing.T) {
	ctx, db := openDB(t)
	// Canonical Jaro/Jaro-Winkler example: MARTHA vs MARHTA.
	if j := scanFloat(t, ctx, db, `SELECT jaro('MARTHA', 'MARHTA')`); math.Abs(j-0.944444) > 1e-5 {
		t.Errorf("jaro(MARTHA,MARHTA) = %f, want ~0.94444", j)
	}
	if jw := scanFloat(t, ctx, db, `SELECT jaro_winkler('MARTHA', 'MARHTA')`); math.Abs(jw-0.961111) > 1e-5 {
		t.Errorf("jaro_winkler(MARTHA,MARHTA) = %f, want ~0.96111", jw)
	}
	if got := scanFloat(t, ctx, db, `SELECT jaro_winkler('abc', 'abc')`); got != 1 {
		t.Errorf("jaro_winkler(abc,abc) = %f, want 1", got)
	}
	if got := scanFloat(t, ctx, db, `SELECT jaro_winkler('abc', 'xyz')`); got != 0 {
		t.Errorf("jaro_winkler(abc,xyz) = %f, want 0", got)
	}
}

func TestFuzzy_Soundex(t *testing.T) {
	ctx, db := openDB(t)
	cases := map[string]string{
		"Robert":   "R163",
		"Rupert":   "R163",
		"Tymczak":  "T522",
		"Pfister":  "P236",
		"Ashcraft": "A261",
		"Honeyman": "H555",
	}
	for in, want := range cases {
		if got := scanStr(t, ctx, db, `SELECT soundex(?)`, in); got != want {
			t.Errorf("soundex(%q) = %q, want %q", in, got, want)
		}
	}
	if got := scanStr(t, ctx, db, `SELECT soundex('123')`); got != "" {
		t.Errorf("soundex of non-letters = %q, want empty", got)
	}
}

var _ func(*sqlite.Conn) error = fuzzy.Register
