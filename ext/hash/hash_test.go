package hash_test

import (
	"context"
	_ "crypto/md5"  //nolint:gosec
	_ "crypto/sha1" //nolint:gosec
	_ "crypto/sha256"
	_ "crypto/sha512"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	_ "golang.org/x/crypto/blake2b"
	_ "golang.org/x/crypto/blake2s"
	//lint:ignore SA1019 test imports cover full hash matrix including deprecated
	_ "golang.org/x/crypto/md4" //nolint:gosec,staticcheck
	//lint:ignore SA1019 test imports cover full hash matrix including deprecated
	_ "golang.org/x/crypto/ripemd160" //nolint:gosec,staticcheck
	_ "golang.org/x/crypto/sha3"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/hash"
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
		t.Fatalf("Conn: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return hash.Register(c)
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return db, sc
}

func hashHex(t *testing.T, sc *sql.Conn, q string, args ...any) string {
	t.Helper()
	var b []byte
	if err := sc.QueryRowContext(context.Background(), q, args...).Scan(&b); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return hex.EncodeToString(b)
}

func TestHash_KnownVectors(t *testing.T) {
	// Empty-input vectors for each algorithm — the canonical "did we wire
	// the right hash?" smoke test.
	_, sc := openDB(t)
	cases := []struct {
		fn   string
		want string // hex of digest("")
	}{
		{"md5('')", "d41d8cd98f00b204e9800998ecf8427e"},
		{"sha1('')", "da39a3ee5e6b4b0d3255bfef95601890afd80709"},
		{"sha224('')", "d14a028c2a3a2bc9476102bb288234c415a2b01f828ea62ac5b3e42f"},
		{"sha256('')", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"sha384('')", "38b060a751ac96384cd9327eb1b1e36a21fdb71114be07434c0cc7bf63f6e1da274edebfe76f65fbd51ad2f14898b95b"},
		{"sha512('')", "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e"},
		{"sha3('')", "a7ffc6f8bf1ed76651c14756a061d662f580ff4de43b49fa82d80a4b80f8434a"},
		{"blake2b('')", "786a02f742015903c6c6fd852552d272912f4740e15847618a86e217f71f5419d25e1031afee585313896444934eb04b903a685b1448b755d56f701afe9be2ce"},
	}
	for _, tc := range cases {
		got := hashHex(t, sc, "SELECT "+tc.fn)
		if got != tc.want {
			t.Errorf("%s = %s, want %s", tc.fn, got, tc.want)
		}
	}
}

func TestHash_SizeVariants(t *testing.T) {
	_, sc := openDB(t)
	cases := []struct {
		q       string
		wantLen int // bytes
	}{
		{`SELECT sha256('hello', 224)`, 28},
		{`SELECT sha256('hello', 256)`, 32},
		{`SELECT sha512('hello', 384)`, 48},
		{`SELECT sha512('hello', 512)`, 64},
		{`SELECT sha3('hello', 224)`, 28},
		{`SELECT sha3('hello', 512)`, 64},
		{`SELECT blake2b('hello', 256)`, 32},
		{`SELECT blake2b('hello', 384)`, 48},
	}
	for _, tc := range cases {
		var b []byte
		if err := sc.QueryRowContext(context.Background(), tc.q).Scan(&b); err != nil {
			t.Fatalf("%s: %v", tc.q, err)
		}
		if len(b) != tc.wantLen {
			t.Errorf("%s: len=%d, want %d", tc.q, len(b), tc.wantLen)
		}
	}
}

func TestHash_InvalidSize(t *testing.T) {
	_, sc := openDB(t)
	_, err := sc.ExecContext(context.Background(), `SELECT sha256('hello', 999)`)
	if err == nil || !strings.Contains(err.Error(), "invalid size") {
		t.Errorf("got %v, want invalid-size error", err)
	}
}

func TestHash_BlobInput(t *testing.T) {
	// Hash functions accept BLOB and TEXT identically because the Go
	// signature uses []byte. The wrapper just rounds the SQL value to
	// bytes.
	_, sc := openDB(t)
	got := hashHex(t, sc, `SELECT sha256(?)`, []byte("hello"))
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Errorf("sha256(blob)=%s, want %s", got, want)
	}
}
