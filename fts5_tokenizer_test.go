package sqlite

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
)

func ftsWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func reverseASCII(s string) string {
	b := []byte(s)
	for l, r := 0, len(b)-1; l < r; l, r = l+1, r-1 {
		b[l], b[r] = b[r], b[l]
	}
	return string(b)
}

// reverseTokenizer emits each word reversed and lowercased, with the source
// word's real byte offsets. No built-in tokenizer reverses, so a MATCH on the
// reversed form (and the miss on the forward form) unambiguously proves the Go
// tokenizer drove both indexing and querying.
type reverseTokenizer struct{}

func (reverseTokenizer) Tokenize(text string, emit func(string, int, int) error) error {
	for i := 0; i < len(text); {
		for i < len(text) && !ftsWordByte(text[i]) {
			i++
		}
		start := i
		for i < len(text) && ftsWordByte(text[i]) {
			i++
		}
		if i > start {
			if err := emit(reverseASCII(strings.ToLower(text[start:i])), start, i); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestRegisterFTS5Tokenizer(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()

	if err := c.RegisterFTS5Tokenizer("gotok", func(args []string) (FTS5Tokenizer, error) {
		return reverseTokenizer{}, nil
	}); err != nil {
		t.Fatalf("RegisterFTS5Tokenizer: %v", err)
	}
	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE docs USING fts5(body, tokenize='gotok')`); err != nil {
		t.Fatalf("create fts5 with custom tokenizer: %v", err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO docs(body) VALUES ('Hello World')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Inspect the actual indexed terms via fts5vocab: the reverse tokenizer must
	// have stored "hello"/"world" reversed+lowercased as "olleh"/"dlrow". (MATCH
	// alone can't show this — FTS5 runs the tokenizer on the query too, so a
	// reversible transform round-trips and is invisible to it.)
	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE vocab USING fts5vocab('docs', 'row')`); err != nil {
		t.Fatalf("create fts5vocab: %v", err)
	}
	rows, err := sc.QueryContext(ctx, `SELECT term FROM vocab ORDER BY term`)
	if err != nil {
		t.Fatalf("vocab query: %v", err)
	}
	defer rows.Close()
	var terms []string
	for rows.Next() {
		var term string
		if err := rows.Scan(&term); err != nil {
			t.Fatal(err)
		}
		terms = append(terms, term)
	}
	if want := []string{"dlrow", "olleh"}; !slices.Equal(terms, want) {
		t.Errorf("indexed terms = %v, want %v (custom reverse tokenizer not applied at index time)", terms, want)
	}

	// End-to-end query path: a forward query round-trips (query 'hello' is also
	// reversed to 'olleh', which is what's stored) and matches the document.
	var n int
	if err := sc.QueryRowContext(ctx, `SELECT count(*) FROM docs WHERE docs MATCH 'hello'`).Scan(&n); err != nil {
		t.Fatalf("MATCH: %v", err)
	}
	if n != 1 {
		t.Errorf("MATCH 'hello' = %d, want 1 (query-side tokenization round-trip)", n)
	}
}

// TestRegisterFTS5Tokenizer_Args: arguments after the name in tokenize='name a b'
// are delivered to the factory.
func TestRegisterFTS5Tokenizer_Args(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	var gotArgs []string
	if err := c.RegisterFTS5Tokenizer("argtok", func(args []string) (FTS5Tokenizer, error) {
		gotArgs = args
		return reverseTokenizer{}, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE d USING fts5(body, tokenize='argtok foo bar')`); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The factory is invoked lazily on first tokenize; force it.
	if _, err := sc.ExecContext(ctx, `INSERT INTO d(body) VALUES ('x')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if want := []string{"foo", "bar"}; !slices.Equal(gotArgs, want) {
		t.Errorf("factory args = %v, want %v", gotArgs, want)
	}
}

// TestRegisterFTS5Tokenizer_NilFactory: a nil factory is rejected up front.
func TestRegisterFTS5Tokenizer_NilFactory(t *testing.T) {
	_, _, c := withSQLite3Conn(t, ":memory:")
	if err := c.RegisterFTS5Tokenizer("x", nil); err == nil {
		t.Error("RegisterFTS5Tokenizer with a nil factory should error")
	}
}

// TestRegisterFTS5Tokenizer_DrainOnClose: the factory id is reclaimed on conn
// close (per-conn registration must not leak).
func TestRegisterFTS5Tokenizer_DrainOnClose(t *testing.T) {
	base := func() int {
		ftsTokFactories.mu.RLock()
		defer ftsTokFactories.mu.RUnlock()
		return len(ftsTokFactories.m)
	}
	start := base()

	db, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	sc, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Raw(func(dc any) error {
		return dc.(*Conn).RegisterFTS5Tokenizer("gotok2", func([]string) (FTS5Tokenizer, error) {
			return reverseTokenizer{}, nil
		})
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := base(); got != start+1 {
		t.Fatalf("after register: registry len = %d, want %d", got, start+1)
	}
	_ = sc.Close()
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := base(); got != start {
		t.Errorf("registry not drained on close: have %d, want %d", got, start)
	}
}
