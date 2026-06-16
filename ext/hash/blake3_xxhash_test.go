package hash_test

import (
	"context"
	"database/sql"
	"testing"
)

func scanInt(t *testing.T, sc *sql.Conn, q string) int {
	t.Helper()
	var n int
	if err := sc.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestHash_Blake3(t *testing.T) {
	_, sc := openDB(t)
	// Canonical BLAKE3 empty-input digest (256-bit / 32-byte).
	if got := hashHex(t, sc, `SELECT blake3('')`); got != "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262" {
		t.Errorf("blake3('') = %s, want the canonical empty digest", got)
	}
	// Default output is 32 bytes; an explicit size selects the XOF length.
	if n := scanInt(t, sc, `SELECT length(blake3('hello'))`); n != 32 {
		t.Errorf("blake3 default length = %d, want 32", n)
	}
	if n := scanInt(t, sc, `SELECT length(blake3('hello', 64))`); n != 64 {
		t.Errorf("blake3(_, 64) length = %d, want 64", n)
	}
	// The 32-byte XOF form must equal the Sum256 fast path.
	if a, b := hashHex(t, sc, `SELECT blake3('hello')`), hashHex(t, sc, `SELECT blake3('hello', 32)`); a != b {
		t.Errorf("blake3('hello') %s != blake3('hello', 32) %s", a, b)
	}
	// Out-of-range size errors rather than panicking inside the UDF.
	var b []byte
	if err := sc.QueryRowContext(context.Background(), `SELECT blake3('x', 0)`).Scan(&b); err == nil {
		t.Error("blake3 with size 0 should error")
	}
}

func TestHash_XXH64(t *testing.T) {
	_, sc := openDB(t)
	// XXH64("") with seed 0 = 0xEF46DB3751D8E999.
	if got := hashHex(t, sc, `SELECT xxh64('')`); got != "ef46db3751d8e999" {
		t.Errorf("xxh64('') = %s, want ef46db3751d8e999", got)
	}
	if n := scanInt(t, sc, `SELECT length(xxh64('hello'))`); n != 8 {
		t.Errorf("xxh64 length = %d, want 8", n)
	}
	if hashHex(t, sc, `SELECT xxh64('a')`) == hashHex(t, sc, `SELECT xxh64('b')`) {
		t.Error("xxh64 collided on distinct inputs 'a' vs 'b'")
	}
}
