package uuid_test

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	gid "github.com/google/uuid"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/uuid"
)

var rfc4122re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func open(t *testing.T) (*sql.DB, *sql.Conn) {
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
		return uuid.Register(c)
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return db, sc
}

func TestUUID_DefaultV4(t *testing.T) {
	_, sc := open(t)
	var s string
	if err := sc.QueryRowContext(context.Background(), `SELECT uuid()`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	if !rfc4122re.MatchString(s) {
		t.Errorf("uuid() returned %q, not RFC 4122 format", s)
	}
	parsed, err := gid.Parse(s)
	if err != nil {
		t.Fatalf("not parseable: %v", err)
	}
	if parsed.Version() != 4 {
		t.Errorf("uuid() default version = %d, want 4", parsed.Version())
	}
}

func TestUUID_AllVersions(t *testing.T) {
	_, sc := open(t)
	ctx := context.Background()
	for _, ver := range []int64{1, 4, 6, 7} {
		var s string
		if err := sc.QueryRowContext(ctx, `SELECT uuid(?)`, ver).Scan(&s); err != nil {
			t.Fatalf("uuid(%d): %v", ver, err)
		}
		parsed, err := gid.Parse(s)
		if err != nil {
			t.Fatalf("uuid(%d): unparseable %q", ver, s)
		}
		if parsed.Version() != gid.Version(ver) {
			t.Errorf("uuid(%d).Version = %d, want %d", ver, parsed.Version(), ver)
		}
	}
}

func TestUUID_NameBased(t *testing.T) {
	_, sc := open(t)
	ctx := context.Background()
	// v5 over the DNS namespace + "example.com" — deterministic.
	var v5 string
	if err := sc.QueryRowContext(ctx,
		`SELECT uuid(5, 'dns', 'example.com')`).Scan(&v5); err != nil {
		t.Fatal(err)
	}
	want := gid.NewSHA1(gid.NameSpaceDNS, []byte("example.com")).String()
	if v5 != want {
		t.Errorf("uuid(5,dns,example.com)=%q, want %q", v5, want)
	}
	// v3 similarly.
	var v3 string
	if err := sc.QueryRowContext(ctx,
		`SELECT uuid(3, 'url', 'https://example.com')`).Scan(&v3); err != nil {
		t.Fatal(err)
	}
	wantV3 := gid.NewMD5(gid.NameSpaceURL, []byte("https://example.com")).String()
	if v3 != wantV3 {
		t.Errorf("uuid(3,url,...)=%q, want %q", v3, wantV3)
	}
}

func TestUUID_GenRandom(t *testing.T) {
	_, sc := open(t)
	var s string
	if err := sc.QueryRowContext(context.Background(), `SELECT gen_random_uuid()`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	parsed, err := gid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version() != 4 {
		t.Errorf("gen_random_uuid() version = %d, want 4", parsed.Version())
	}
}

func TestUUID_ParseAndFormat(t *testing.T) {
	_, sc := open(t)
	canonical := "6ba7b810-9dad-11d1-80b4-00c04fd430c8" // NameSpaceDNS
	ctx := context.Background()

	var str string
	if err := sc.QueryRowContext(ctx, `SELECT uuid_str(?)`, canonical).Scan(&str); err != nil {
		t.Fatal(err)
	}
	if str != canonical {
		t.Errorf("uuid_str = %q, want %q", str, canonical)
	}

	var blob []byte
	if err := sc.QueryRowContext(ctx, `SELECT uuid_blob(?)`, canonical).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if len(blob) != 16 {
		t.Errorf("uuid_blob len=%d, want 16", len(blob))
	}

	// Round-trip BLOB → STR.
	var roundTrip string
	if err := sc.QueryRowContext(ctx, `SELECT uuid_str(?)`, blob).Scan(&roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip != canonical {
		t.Errorf("blob round-trip = %q, want %q", roundTrip, canonical)
	}
}

func TestUUID_ExtractVersion(t *testing.T) {
	_, sc := open(t)
	ctx := context.Background()
	known := map[string]int64{
		"6ba7b810-9dad-11d1-80b4-00c04fd430c8": 1, // ns DNS, v1
		"550e8400-e29b-41d4-a716-446655440000": 4, // canonical v4 example
	}
	for u, ver := range known {
		var got int64
		if err := sc.QueryRowContext(ctx, `SELECT uuid_extract_version(?)`, u).Scan(&got); err != nil {
			t.Fatalf("(%s): %v", u, err)
		}
		if got != ver {
			t.Errorf("uuid_extract_version(%q)=%d, want %d", u, got, ver)
		}
	}
}

func TestUUID_ExtractTimestamp(t *testing.T) {
	_, sc := open(t)
	ctx := context.Background()
	// v1 UUID has an extractable timestamp.
	var s string
	if err := sc.QueryRowContext(ctx, `SELECT uuid(1)`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	var ts sql.NullInt64
	if err := sc.QueryRowContext(ctx,
		`SELECT uuid_extract_timestamp(?)`, s).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if !ts.Valid {
		t.Error("v1 timestamp should be non-NULL")
	}

	// v4 has no extractable timestamp → NULL.
	if err := sc.QueryRowContext(ctx, `SELECT uuid(4)`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	if err := sc.QueryRowContext(ctx,
		`SELECT uuid_extract_timestamp(?)`, s).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if ts.Valid {
		t.Errorf("v4 timestamp should be NULL, got %d", ts.Int64)
	}
}

func TestUUID_BadVersion(t *testing.T) {
	_, sc := open(t)
	_, err := sc.ExecContext(context.Background(), `SELECT uuid(99)`)
	if err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Errorf("got %v, want unsupported version error", err)
	}
}

func TestUUID_NameSpaceShortcuts(t *testing.T) {
	_, sc := open(t)
	ctx := context.Background()
	// Each shortcut should produce the same UUID as the canonical literal.
	want := gid.NewSHA1(gid.NameSpaceDNS, []byte("x")).String()
	for _, shortcut := range []string{"dns", "DNS", "fqdn", "Fqdn"} {
		var got string
		if err := sc.QueryRowContext(ctx,
			`SELECT uuid(5, ?, ?)`, shortcut, "x").Scan(&got); err != nil {
			t.Fatalf("(%s): %v", shortcut, err)
		}
		if got != want {
			t.Errorf("shortcut %q: got %q, want %q", shortcut, got, want)
		}
	}
}
