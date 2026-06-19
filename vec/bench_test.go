package vec_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"testing"

	_ "gosqlite.org"
	"gosqlite.org/vec"
)

// openBenchDB mirrors openDB but takes testing.TB so benchmarks and
// tests can share the fixture without forking it.
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

// genCorpus returns n deterministic float32 vectors of the given
// dimension. A fixed PCG seed keeps benchmark runs comparable.
func genCorpus(n, dim int) []vec.Row {
	r := rand.New(rand.NewPCG(0xC0FFEE, 0xBEEF))
	out := make([]vec.Row, n)
	for i := range out {
		v := make([]float32, dim)
		for j := range v {
			v[j] = float32(r.Float64()*2 - 1)
		}
		out[i] = vec.Row{Rowid: int64(i + 1), Embedding: v}
	}
	return out
}

// genQuery returns a single deterministic query vector.
func genQuery(dim int) []float32 {
	r := rand.New(rand.NewPCG(0xFACADE, 0xBABE))
	v := make([]float32, dim)
	for j := range v {
		v[j] = float32(r.Float64()*2 - 1)
	}
	return v
}

const benchDim = 384

func benchSetup(b *testing.B, corpusSize int, enc vec.Encoding) (*vec.Table, []float32) {
	b.Helper()
	db := openBenchDB(b)
	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "bench", benchDim, vec.Options{
		Metric:   vec.L2,
		Encoding: enc,
	})
	if err != nil {
		b.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, genCorpus(corpusSize, benchDim)); err != nil {
		b.Fatal(err)
	}
	return tbl, genQuery(benchDim)
}

// BenchmarkKNN measures a single KNN call over a 10k-row corpus,
// dim=384, k=10, default options.
func BenchmarkKNN(b *testing.B) {
	tbl, q := benchSetup(b, 10_000, vec.Binary)
	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		out, err := tbl.KNNSlice(ctx, q, 10)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) != 10 {
			b.Fatalf("got %d hits, want 10", len(out))
		}
	}
}

// BenchmarkKNN_WithFilter measures KNN with a rowid-IN filter on the
// same corpus. sqlite-vec's planner accepts MATCH alongside `rowid IN
// (…)` or `rowid = ?` but rejects open-ended range predicates because
// it can't extract a `LIMIT` constraint from them.
func BenchmarkKNN_WithFilter(b *testing.B) {
	tbl, q := benchSetup(b, 10_000, vec.Binary)
	ctx := context.Background()
	// 20-element IN list. Tight enough to force the filter onto the
	// hot path; large enough that planner overhead is meaningful.
	allowed := make([]any, 20)
	placeholders := ""
	for i := range allowed {
		allowed[i] = int64(i*500 + 1)
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
	}
	filter := "rowid IN (" + placeholders + ")"
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		out, err := tbl.KNNSlice(ctx, q, 10, vec.WithFilter(filter, allowed...))
		if err != nil {
			b.Fatal(err)
		}
		if len(out) == 0 {
			b.Fatal("no hits")
		}
	}
}

// BenchmarkBatchInsert_JSON measures a 1000-row BatchInsert with the
// JSON wire encoding (text fragments per row).
func BenchmarkBatchInsert_JSON(b *testing.B) {
	corpus := genCorpus(1000, benchDim)
	for i := range corpus {
		corpus[i].Rowid = int64(i + 1)
	}
	ctx := context.Background()
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		// Fresh table per iteration (vec0 INSERT rejects duplicate
		// rowids and we want N independent batches measured); the
		// CREATE VIRTUAL TABLE cost is excluded from the timed window.
		b.StopTimer()
		db := openBenchDB(b)
		tbl, err := vec.Create(ctx, db, fmt.Sprintf("bench_%d", n), benchDim, vec.Options{
			Encoding: vec.JSON,
		})
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := tbl.BatchInsert(ctx, corpus); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBatchInsert_Binary mirrors the JSON variant with the binary
// wire encoding (packed float32 BLOB per row).
func BenchmarkBatchInsert_Binary(b *testing.B) {
	corpus := genCorpus(1000, benchDim)
	for i := range corpus {
		corpus[i].Rowid = int64(i + 1)
	}
	ctx := context.Background()
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		db := openBenchDB(b)
		tbl, err := vec.Create(ctx, db, fmt.Sprintf("bench_%d", n), benchDim, vec.Options{
			Encoding: vec.Binary,
		})
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := tbl.BatchInsert(ctx, corpus); err != nil {
			b.Fatal(err)
		}
	}
}
