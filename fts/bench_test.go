package fts_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/fts"
)

// openBenchDB mirrors openDB but takes testing.TB.
func openBenchDB(tb testing.TB) *sql.DB {
	tb.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	return db
}

// benchDocs returns n FTS5 documents, each ~30 short English words.
// The corpus is deterministic so benchmark runs are comparable.
func benchDocs(n int) []fts.Attr[int64, string] {
	words := []string{
		"the", "quick", "brown", "fox", "jumps", "over", "lazy", "dog",
		"pack", "my", "box", "with", "five", "dozen", "liquor", "jugs",
		"sphinx", "of", "black", "quartz", "judge", "vow", "amazingly",
		"few", "discotheques", "provide", "jukeboxes", "how", "are", "you",
	}
	out := make([]fts.Attr[int64, string], n)
	for i := range out {
		buf := make([]byte, 0, 200)
		for j := range 30 {
			if j > 0 {
				buf = append(buf, ' ')
			}
			buf = append(buf, words[(i*37+j*13)%len(words)]...)
		}
		out[i] = fts.Attr[int64, string]{Key: int64(i + 1), Value: string(buf)}
	}
	return out
}

func benchSetup(b *testing.B, corpusSize int) *fts.Index[int64, string] {
	b.Helper()
	db := openBenchDB(b)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
	})
	if err != nil {
		b.Fatal(err)
	}
	if err := idx.Insert(ctx, benchDocs(corpusSize)...); err != nil {
		b.Fatal(err)
	}
	return idx
}

// BenchmarkSearch measures a Term search over a 10k-document corpus
// with no ranking — the default fast path.
func BenchmarkSearch(b *testing.B) {
	idx := benchSetup(b, 10_000)
	ctx := context.Background()
	q := fts.Term("fox")
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		hits, err := idx.SearchSlice(ctx, q, fts.WithLimit(10))
		if err != nil {
			b.Fatal(err)
		}
		if len(hits) == 0 {
			b.Fatal("no hits")
		}
	}
}

// BenchmarkSearch_WithRanking measures the same query with BM25
// ranking enabled and a per-column weight, which forces the SQL
// builder to push extra args and select an aliased rank column.
func BenchmarkSearch_WithRanking(b *testing.B) {
	idx := benchSetup(b, 10_000)
	ctx := context.Background()
	q := fts.Term("fox")
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		hits, err := idx.SearchSlice(ctx, q,
			fts.WithLimit(10), fts.WithRanking(1.0))
		if err != nil {
			b.Fatal(err)
		}
		if len(hits) == 0 {
			b.Fatal("no hits")
		}
	}
}

// BenchmarkInsert measures inserting a 1000-doc batch in one Insert
// call (which uses a single transaction internally). Each iteration
// uses a fresh table; setup is excluded from the timed window via
// StopTimer / StartTimer so the measurement is the insert cost, not
// the CREATE VIRTUAL TABLE cost.
func BenchmarkInsert(b *testing.B) {
	docs := benchDocs(1000)
	ctx := context.Background()
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		db := openBenchDB(b)
		idx, err := fts.New[int64, string](ctx, db, fmt.Sprintf("bench_%d", n), fts.Options{
			Tokenizer: fts.Porter{Base: fts.Unicode61{}},
		})
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := idx.Insert(ctx, docs...); err != nil {
			b.Fatal(err)
		}
	}
}
