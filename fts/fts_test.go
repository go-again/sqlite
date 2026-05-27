package fts_test

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/fts"
)

// fixtureCorpus is a small but linguistically varied dataset used across
// FTS tests so each test can pick the queries it cares about without
// re-defining seed data.
var fixtureCorpus = []fts.Attr[int64, string]{
	{Key: 1, Value: "the quick brown fox jumps over the lazy dog"},
	{Key: 2, Value: "a brown dog barked at the moon"},
	{Key: 3, Value: "Pack my box with five dozen liquor jugs"},
	{Key: 4, Value: "running rivers run through ridges"},
	{Key: 5, Value: "café résumé naïve façade"},
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1) // FTS5 virtual tables are per-conn.
	return db
}

func newIdx(t *testing.T, db *sql.DB, opts fts.Options) *fts.Index[int64, string] {
	t.Helper()
	idx, err := fts.New[int64, string](context.Background(), db, "docs", opts)
	if err != nil {
		t.Fatalf("fts.New: %v", err)
	}
	return idx
}

// TestNew_DefaultOptions creates the simplest possible index, inserts a few
// rows, and confirms a Term query returns the matching rowid(s).
func TestNew_DefaultOptions(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})

	if err := idx.Insert(ctx, fixtureCorpus...); err != nil {
		t.Fatal(err)
	}
	matches, err := idx.SearchSlice(ctx, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Key != 1 {
		t.Errorf("Term('fox') matches=%+v, want [{Key:1}]", matches)
	}
}

// TestQuery_Phrase asserts that a Phrase query matches the exact ordered
// sequence and not a permutation.
func TestQuery_Phrase(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	idx.Insert(ctx, fixtureCorpus...)

	matches, err := idx.SearchSlice(ctx, fts.Phrase("brown", "dog"))
	if err != nil {
		t.Fatal(err)
	}
	keys := keysOf(matches)
	if !slices.Contains(keys, int64(2)) {
		t.Errorf("Phrase('brown dog') should match doc 2; got %v", keys)
	}
	if slices.Contains(keys, int64(1)) {
		t.Errorf("Phrase('brown dog') should NOT match doc 1 (different order); got %v", keys)
	}
}

// TestQuery_Prefix checks the FTS5 `term*` prefix operator.
func TestQuery_Prefix(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	idx.Insert(ctx, fixtureCorpus...)

	matches, err := idx.SearchSlice(ctx, fts.Prefix("ru"))
	if err != nil {
		t.Fatal(err)
	}
	keys := keysOf(matches)
	if !slices.Contains(keys, int64(4)) {
		t.Errorf("Prefix('ru') should match doc 4; got %v", keys)
	}
}

// TestQuery_And asserts that AND requires both terms.
func TestQuery_And(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	idx.Insert(ctx, fixtureCorpus...)

	matches, err := idx.SearchSlice(ctx, fts.And(fts.Term("brown"), fts.Term("dog")))
	if err != nil {
		t.Fatal(err)
	}
	keys := keysOf(matches)
	// Both docs 1 and 2 mention brown AND dog (different orders).
	if !slices.Contains(keys, int64(1)) || !slices.Contains(keys, int64(2)) {
		t.Errorf("AND(brown, dog) should match docs 1 and 2; got %v", keys)
	}
}

// TestQuery_Or asserts union semantics.
func TestQuery_Or(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	idx.Insert(ctx, fixtureCorpus...)

	matches, err := idx.SearchSlice(ctx, fts.Or(fts.Term("fox"), fts.Term("moon")))
	if err != nil {
		t.Fatal(err)
	}
	keys := keysOf(matches)
	if !slices.Contains(keys, int64(1)) || !slices.Contains(keys, int64(2)) {
		t.Errorf("OR(fox, moon) should match docs 1 and 2; got %v", keys)
	}
}

// TestQuery_Not excludes negative terms.
func TestQuery_Not(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	idx.Insert(ctx, fixtureCorpus...)

	matches, err := idx.SearchSlice(ctx,
		fts.Not(fts.Term("brown"), fts.Term("fox")))
	if err != nil {
		t.Fatal(err)
	}
	keys := keysOf(matches)
	if !slices.Contains(keys, int64(2)) {
		t.Errorf("NOT(brown, fox) should match doc 2; got %v", keys)
	}
	if slices.Contains(keys, int64(1)) {
		t.Errorf("NOT(brown, fox) should NOT match doc 1; got %v", keys)
	}
}

// TestSearch_WithRanking populates BM25 weights and asserts the result list
// is non-empty and rank-ordered.
func TestSearch_WithRanking(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	idx.Insert(ctx, fixtureCorpus...)

	matches, err := idx.SearchSlice(ctx,
		fts.Or(fts.Term("brown"), fts.Term("dog")),
		fts.WithRanking())
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) < 2 {
		t.Fatalf("expected ≥2 ranked matches, got %d", len(matches))
	}
	// Smaller (more negative) BM25 == better. Confirm monotonic.
	for i := 1; i < len(matches); i++ {
		if matches[i].Rank < matches[i-1].Rank {
			t.Errorf("rank order violated at [%d]: %v", i, matches)
		}
	}
}

// TestSearch_RankingWithColumnWeights confirms that per-column BM25
// weights shift the result order. We index two rows where each contains the
// query term once — once in the title, once in the body — then query under
// two weight configurations:
//   - title weight=10, body weight=1 → title-bearing row ranks first
//   - title weight=1, body weight=10 → body-bearing row ranks first
//
// Catches regressions in WithRanking's varargs threading through the SQL.
func TestSearch_RankingWithColumnWeights(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Columns: []string{"title", "body"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Doc 1: "fox" only in title. Doc 2: "fox" only in body.
	err = idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "fox news", Extras: map[string]any{"body": "a story about animals"}},
		fts.Attr[int64, string]{Key: 2, Value: "animals", Extras: map[string]any{"body": "a fox sighting"}},
	)
	if err != nil {
		t.Fatal(err)
	}

	// Weight 1: title heavy → doc 1 (title-match) ranks first.
	titleHeavy, err := idx.SearchSlice(ctx, fts.Term("fox"), fts.WithRanking(10.0, 1.0))
	if err != nil {
		t.Fatal(err)
	}
	if len(titleHeavy) < 2 || titleHeavy[0].Key != 1 {
		t.Errorf("title-heavy weights: first match=%v, want Key=1; full=%v",
			titleHeavy[0], titleHeavy)
	}

	// Weight 2: body heavy → doc 2 (body-match) ranks first.
	bodyHeavy, err := idx.SearchSlice(ctx, fts.Term("fox"), fts.WithRanking(1.0, 10.0))
	if err != nil {
		t.Fatal(err)
	}
	if len(bodyHeavy) < 2 || bodyHeavy[0].Key != 2 {
		t.Errorf("body-heavy weights: first match=%v, want Key=2; full=%v",
			bodyHeavy[0], bodyHeavy)
	}
}

// TestSearch_SnippetAndHighlight requests both snippet and highlight outputs
// and confirms they wrap matched terms with the configured delimiters.
func TestSearch_SnippetAndHighlight(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	idx.Insert(ctx, fixtureCorpus...)

	matches, err := idx.SearchSlice(ctx,
		fts.Term("fox"),
		fts.WithSnippet("value", "[", "]", "…", 10),
		fts.WithHighlight("value", "<b>", "</b>"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("want 1 match, got %d", len(matches))
	}
	if !strings.Contains(matches[0].Snippet, "[fox]") {
		t.Errorf("snippet=%q does not contain [fox]", matches[0].Snippet)
	}
	if !strings.Contains(matches[0].Highlight, "<b>fox</b>") {
		t.Errorf("highlight=%q does not contain <b>fox</b>", matches[0].Highlight)
	}
}

// TestSearch_LimitOffset confirms LIMIT/OFFSET pagination.
func TestSearch_LimitOffset(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	for i := int64(1); i <= 10; i++ {
		idx.Insert(ctx, fts.Attr[int64, string]{Key: i, Value: "alpha"})
	}
	matches, err := idx.SearchSlice(ctx, fts.Term("alpha"), fts.WithLimit(3), fts.WithOffset(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Errorf("limit=3 offset=2 returned %d rows, want 3", len(matches))
	}
}

// TestTokenizer_Ascii asserts the ASCII tokenizer indexes plain words case-
// insensitively (uppercase queries match lowercased tokens) and ignores the
// Unicode-specific features Unicode61 brings. Used by callers who know their
// corpus is pure 7-bit ASCII and want the cheapest possible tokenizer.
func TestTokenizer_Ascii(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{Tokenizer: fts.Ascii{}})

	if err := idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "hello world"},
		fts.Attr[int64, string]{Key: 2, Value: "Goodbye Galaxy"},
	); err != nil {
		t.Fatal(err)
	}
	// Case-insensitive: uppercase query hits lowercased token.
	matches, err := idx.SearchSlice(ctx, fts.Term("GALAXY"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(keysOf(matches), int64(2)) {
		t.Errorf("Ascii GALAXY should match doc 2; got %v", keysOf(matches))
	}
	// Diacritics are NOT folded by Ascii (that's Unicode61's job) — a query
	// for an ASCII letter should not match a diacritic-bearing variant.
	if err := idx.Insert(ctx, fts.Attr[int64, string]{Key: 3, Value: "café"}); err != nil {
		t.Fatal(err)
	}
	matches, err = idx.SearchSlice(ctx, fts.Term("cafe"))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(keysOf(matches), int64(3)) {
		t.Errorf("Ascii('cafe') should NOT match 'café' (no diacritic fold); got %v", keysOf(matches))
	}
}

// TestTokenizer_Porter verifies stemming: "running" should match "run".
func TestTokenizer_Porter(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{Tokenizer: fts.Porter{Base: fts.Unicode61{}}})
	idx.Insert(ctx, fixtureCorpus...)

	matches, err := idx.SearchSlice(ctx, fts.Term("run"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(keysOf(matches), int64(4)) {
		t.Errorf("Porter('run') should match doc 4 (runs, running); got %v", keysOf(matches))
	}
}

// TestTokenizer_Unicode61_RemoveDiacritics confirms diacritic-stripping
// makes "cafe" find "café".
func TestTokenizer_Unicode61_RemoveDiacritics(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{Tokenizer: fts.Unicode61{RemoveDiacritics: 2}})
	idx.Insert(ctx, fixtureCorpus...)

	matches, err := idx.SearchSlice(ctx, fts.Term("cafe"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(keysOf(matches), int64(5)) {
		t.Errorf("Unicode61(remove_diacritics=2) on 'cafe' should match doc 5; got %v", keysOf(matches))
	}
}

// TestTokenizer_Trigram exercises substring search via the trigram tokenizer.
func TestTokenizer_Trigram(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{Tokenizer: fts.Trigram{}})
	idx.Insert(ctx, fixtureCorpus...)

	// "iquor" (substring of "liquor") should hit doc 3 with trigram.
	matches, err := idx.SearchSlice(ctx, fts.Term("iquor"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(keysOf(matches), int64(3)) {
		t.Errorf("trigram('iquor') should match doc 3; got %v", keysOf(matches))
	}
}

// TestIndex_Delete removes a row and confirms it no longer matches.
func TestIndex_Delete(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	idx.Insert(ctx, fixtureCorpus...)
	if err := idx.Delete(ctx, 1); err != nil {
		t.Fatal(err)
	}
	matches, _ := idx.SearchSlice(ctx, fts.Term("fox"))
	if len(matches) != 0 {
		t.Errorf("after delete, Term('fox') should match nothing; got %v", matches)
	}
}

// TestIndex_OptimizeAndMerge ensures the maintenance INSERTs do not error.
func TestIndex_OptimizeAndMerge(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	idx.Insert(ctx, fixtureCorpus...)
	if err := idx.Optimize(ctx); err != nil {
		t.Errorf("Optimize: %v", err)
	}
	if err := idx.Merge(ctx, 1); err != nil {
		t.Errorf("Merge: %v", err)
	}
}

// TestSearch_StreamingBreak confirms iter.Seq2 cleanup after early break.
func TestSearch_StreamingBreak(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	idx.Insert(ctx, fixtureCorpus...)
	count := 0
	for _, err := range idx.Search(ctx, fts.Or(fts.Term("brown"), fts.Term("dog"))) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		break
	}
	if count != 1 {
		t.Fatalf("expected 1 iteration before break, got %d", count)
	}
	// DB still healthy?
	if _, err := idx.SearchSlice(ctx, fts.Term("fox")); err != nil {
		t.Errorf("query after break failed: %v", err)
	}
}

// TestIndex_MultiColumn exercises a 2-column FTS5 table with Extras.
func TestIndex_MultiColumn(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Columns: []string{"title", "body"},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = idx.Insert(ctx,
		fts.Attr[int64, string]{
			Key:    1,
			Value:  "fox news",
			Extras: map[string]any{"body": "quick brown fox"},
		},
		fts.Attr[int64, string]{
			Key:    2,
			Value:  "dog news",
			Extras: map[string]any{"body": "lazy dog"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Column-scoped query.
	matches, err := idx.SearchSlice(ctx, fts.Column("body", fts.Term("fox")))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Key != 1 {
		t.Errorf("Column('body', 'fox') matches=%+v", matches)
	}
	if got, ok := matches[0].Extras["body"].(string); !ok || !strings.Contains(got, "fox") {
		t.Errorf("Extras['body']=%v, want string containing 'fox'", matches[0].Extras["body"])
	}
}

// keysOf is a small helper that extracts the Key field from every Hit.
func keysOf(matches []fts.Hit[int64, string]) []int64 {
	out := make([]int64, len(matches))
	for i, m := range matches {
		out[i] = m.Key
	}
	return out
}
