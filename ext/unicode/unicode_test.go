package unicode_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/unicode"
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
		return unicode.Register(c)
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return db, sc
}

func TestUnicode_UpperLower(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	cases := []struct {
		q, want string
	}{
		{`SELECT upper('café')`, "CAFÉ"},
		{`SELECT lower('CAFÉ')`, "café"},
		{`SELECT upper('straße', 'de')`, "STRASSE"}, // German eszett → SS (locale-aware)
		{`SELECT upper('straße')`, "STRAßE"},        // no-locale uses strings.ToUpper (1:1)
		{`SELECT lower('ÄPFEL')`, "äpfel"},
		{`SELECT upper('αβγ')`, "ΑΒΓ"}, // Greek
	}
	for _, tc := range cases {
		var got string
		if err := sc.QueryRowContext(ctx, tc.q).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.q, err)
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.q, got, tc.want)
		}
	}
}

func TestUnicode_LocaleAwareUpper(t *testing.T) {
	// Turkish capital dotted I → lowercase dotted i (vs ASCII rule that
	// gives "i" without the dot).
	_, sc := openDB(t)
	var got string
	if err := sc.QueryRowContext(context.Background(),
		`SELECT lower('İSTANBUL', 'tr')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	// Turkish lower of 'İ' is "i" (regular dotted i), and 'I' becomes
	// 'ı' (dotless). So "İSTANBUL" → "istanbul" in Turkish-aware lower.
	if got != "istanbul" {
		t.Errorf("lower(İSTANBUL, tr) = %q, want %q", got, "istanbul")
	}
}

func TestUnicode_InitCap(t *testing.T) {
	_, sc := openDB(t)
	var got string
	if err := sc.QueryRowContext(context.Background(),
		`SELECT initcap('hello world from go')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "Hello World From Go" {
		t.Errorf("initcap=%q, want %q", got, "Hello World From Go")
	}
}

func TestUnicode_Casefold(t *testing.T) {
	// German eszett: casefold('GROẞER') should produce a string that
	// compares equal to "großer" under simple ==.
	_, sc := openDB(t)
	var got1, got2 string
	ctx := context.Background()
	if err := sc.QueryRowContext(ctx, `SELECT casefold('GROẞER')`).Scan(&got1); err != nil {
		t.Fatal(err)
	}
	if err := sc.QueryRowContext(ctx, `SELECT casefold('großer')`).Scan(&got2); err != nil {
		t.Fatal(err)
	}
	if got1 != got2 {
		t.Errorf("casefold mismatch: %q vs %q", got1, got2)
	}
}

func TestUnicode_Unaccent(t *testing.T) {
	_, sc := openDB(t)
	cases := []struct {
		in, want string
	}{
		{"café", "cafe"},
		{"naïve", "naive"},
		{"résumé", "resume"},
		{"Björk", "Bjork"},
		{"日本", "日本"}, // CJK passes through unchanged
	}
	for _, tc := range cases {
		var got string
		if err := sc.QueryRowContext(context.Background(),
			`SELECT unaccent(?)`, tc.in).Scan(&got); err != nil {
			t.Fatalf("(%s): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("unaccent(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUnicode_Normalize(t *testing.T) {
	// NFD decomposes 'é' into 'e' + combining acute (2 codepoints, 3
	// bytes). NFC composes (1 codepoint, 2 bytes).
	_, sc := openDB(t)
	ctx := context.Background()
	var nfc, nfd string
	if err := sc.QueryRowContext(ctx, `SELECT normalize('café', 'NFC')`).Scan(&nfc); err != nil {
		t.Fatal(err)
	}
	if err := sc.QueryRowContext(ctx, `SELECT normalize('café', 'NFD')`).Scan(&nfd); err != nil {
		t.Fatal(err)
	}
	if len(nfc) == len(nfd) {
		t.Errorf("NFC (%d bytes) and NFD (%d bytes) should differ", len(nfc), len(nfd))
	}
}

func TestUnicode_NormalizeBadForm(t *testing.T) {
	_, sc := openDB(t)
	_, err := sc.ExecContext(context.Background(), `SELECT normalize('x', 'XYZ')`)
	if err == nil || !strings.Contains(err.Error(), "invalid form") {
		t.Errorf("got %v, want invalid-form error", err)
	}
}

func TestUnicode_NoCaseUnicodeCollation(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE t(s TEXT COLLATE NOCASE_UNICODE);
		 INSERT INTO t(s) VALUES ('café'), ('CAFÉ'), ('cafe')`); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(context.Background(),
		`SELECT s FROM t WHERE s = 'CAFÉ' COLLATE NOCASE_UNICODE`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	// Both "café" and "CAFÉ" match under NOCASE_UNICODE (same accent,
	// only case differs). "cafe" does NOT match — different combining marks.
	if len(got) != 2 {
		t.Errorf("got %v, want 2 rows ['café','CAFÉ']", got)
	}
}

func TestUnicode_NoCaseAccentCollation(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE t(s TEXT COLLATE NOCASE_ACCENT);
		 INSERT INTO t(s) VALUES ('café'), ('CAFE'), ('résumé')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := sc.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t WHERE s = 'cafe' COLLATE NOCASE_ACCENT`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 rows matching 'cafe' under NOCASE_ACCENT, got %d", n)
	}
}

func TestUnicode_LocaleCollation(t *testing.T) {
	_, sc := openDB(t)
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return unicode.RegisterLocaleCollation(c, "sv", "SV")
	}); err != nil {
		t.Fatalf("RegisterLocaleCollation: %v", err)
	}
	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE t(s TEXT);
		 INSERT INTO t(s) VALUES ('z'), ('å'), ('a')`); err != nil {
		t.Fatal(err)
	}
	// Swedish: 'å' sorts AFTER 'z'. Under default binary collation,
	// 'å' sorts before 'z' because it's lower UTF-8.
	rows, err := sc.QueryContext(context.Background(),
		`SELECT s FROM t ORDER BY s COLLATE SV`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	if len(got) != 3 || got[len(got)-1] != "å" {
		t.Errorf("SV collation ordering: got %v, want last='å'", got)
	}
}

func TestUnicode_LikeOptOut(t *testing.T) {
	// Default Register does NOT install the LIKE override. SQLite's
	// built-in LIKE is ASCII-only, so 'café' LIKE 'CAFÉ' is false.
	_, sc := openDB(t)
	var got bool
	if err := sc.QueryRowContext(context.Background(),
		`SELECT 'café' LIKE 'CAFÉ'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("default LIKE should NOT be Unicode-aware (would mean the override sneaked in)")
	}
}

func TestUnicode_LikeOptIn(t *testing.T) {
	// RegisterLikeOnly directly installs the Unicode-aware LIKE.
	_, sc := openDB(t)
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return unicode.RegisterLikeOnly(c)
	}); err != nil {
		t.Fatalf("RegisterLikeOnly: %v", err)
	}
	cases := []struct {
		text, pat string
		want      bool
	}{
		{"café", "CAFÉ", true},              // case-insensitive
		{"café", "C_FÉ", true},              // _ matches one char
		{"large café shop", "%CAFÉ%", true}, // % matches any run
		{"naïve", "NAIVE", false},           // accent-sensitive
		{"abc", "a%c", true},
	}
	ctx := context.Background()
	for _, tc := range cases {
		var got bool
		if err := sc.QueryRowContext(ctx, `SELECT ? LIKE ?`, tc.text, tc.pat).Scan(&got); err != nil {
			t.Fatalf("%q LIKE %q: %v", tc.text, tc.pat, err)
		}
		if got != tc.want {
			t.Errorf("%q LIKE %q = %v, want %v", tc.text, tc.pat, got, tc.want)
		}
	}
}

// TestUnicode_NullInputs pins that scalar functions pass NULL through
// rather than panicking or returning "".
func TestUnicode_NullInputs(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	for _, fn := range []string{"upper", "lower", "initcap", "casefold", "unaccent", "normalize"} {
		var got sql.NullString
		if err := sc.QueryRowContext(ctx,
			"SELECT "+fn+"(NULL)").Scan(&got); err != nil {
			t.Fatalf("%s(NULL): %v", fn, err)
		}
		if got.Valid {
			t.Errorf("%s(NULL) = %q, want NULL", fn, got.String)
		}
	}
}

// TestUnicode_NoCaseUnicodeOrdering pins that NOCASE_UNICODE drives
// ORDER BY consistently with its equality semantics — case folds
// before comparison, with the secondary order on the raw string for
// deterministic tie-breaking.
func TestUnicode_NoCaseUnicodeOrdering(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(), `
		CREATE TABLE w(s TEXT);
		INSERT INTO w(s) VALUES ('Apple'), ('apple'), ('Banana'), ('banana')`); err != nil {
		t.Fatal(err)
	}
	rows, err := sc.QueryContext(context.Background(),
		`SELECT s FROM w ORDER BY s COLLATE NOCASE_UNICODE, s`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got = append(got, s)
	}
	want := []string{"Apple", "apple", "Banana", "banana"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestUnicode_LikeEscapeChar(t *testing.T) {
	_, sc := openDB(t)
	if err := sc.Raw(func(driverConn any) error {
		c, _ := driverConn.(*sqlite.Conn)
		return unicode.RegisterLikeOnly(c)
	}); err != nil {
		t.Fatal(err)
	}
	// With ESCAPE '\', '\%' matches a literal '%'.
	var got bool
	if err := sc.QueryRowContext(context.Background(),
		`SELECT '50%' LIKE '50\%' ESCAPE '\'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("LIKE with ESCAPE '\\' should match literal '%'")
	}
}
