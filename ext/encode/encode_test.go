package encode_test

import (
	"context"
	"database/sql"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/encode"
	"github.com/go-again/sqlite/internal/testhelp"
)

func openDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	testhelp.WithConnectHook(t, encode.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return context.Background(), db
}

func TestEncode_KnownVectors(t *testing.T) {
	ctx, db := openDB(t)
	cases := []struct{ format, want string }{
		{"base64", "aGVsbG8="},
		{"base64url", "aGVsbG8="},
		{"base32", "NBSWY3DP"},
		{"hex", "68656c6c6f"},
		{"base16", "68656c6c6f"},
	}
	for _, c := range cases {
		var got string
		if err := db.QueryRowContext(ctx, `SELECT encode('hello', ?)`, c.format).Scan(&got); err != nil {
			t.Fatalf("encode(%q): %v", c.format, err)
		}
		if got != c.want {
			t.Errorf("encode('hello', %q) = %q, want %q", c.format, got, c.want)
		}
	}
}

func TestEncode_RoundTrip(t *testing.T) {
	ctx, db := openDB(t)
	for _, format := range []string{"base64", "base64url", "base32", "base32hex", "hex", "ascii85", "url"} {
		var got []byte
		if err := db.QueryRowContext(ctx,
			`SELECT decode(encode('round trip ☺', ?), ?)`, format, format).Scan(&got); err != nil {
			t.Fatalf("round trip %q: %v", format, err)
		}
		if string(got) != "round trip ☺" {
			t.Errorf("%q round trip = %q, want 'round trip ☺'", format, got)
		}
	}
}

func TestEncode_UnknownFormat(t *testing.T) {
	ctx, db := openDB(t)
	var s string
	if err := db.QueryRowContext(ctx, `SELECT encode('x', 'rot13')`).Scan(&s); err == nil {
		t.Error("unknown encode format should error")
	}
	if err := db.QueryRowContext(ctx, `SELECT decode('x', 'rot13')`).Scan(&s); err == nil {
		t.Error("unknown decode format should error")
	}
}

func TestDecode_Malformed(t *testing.T) {
	ctx, db := openDB(t)
	for _, c := range []struct{ text, format string }{
		{"@@@ not base64 @@@", "base64"},
		{"xyz", "hex"}, // odd length, non-hex
		{"8888888", "base32"},
	} {
		var got []byte
		if err := db.QueryRowContext(ctx, `SELECT decode(?, ?)`, c.text, c.format).Scan(&got); err == nil {
			t.Errorf("decode(%q, %q) should error on malformed input", c.text, c.format)
		}
	}
}

var _ func(*sqlite.Conn) error = encode.Register
