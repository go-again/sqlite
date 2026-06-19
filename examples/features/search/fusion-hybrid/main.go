// fusion-hybrid-search example: combine a vec.KNN semantic ranking
// with an fts.Search lexical ranking via Reciprocal Rank Fusion. No
// SQL, no extension to load — just Go.
//
// A real consumer would source vectors from an embedding model; this
// demo keeps the math local with hand-picked vectors so the ranking
// is reproducible.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	"gosqlite.org/fts"
	"gosqlite.org/fusion"
	"gosqlite.org/vec"
)

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // virtual tables are per-conn

	// Vector side: 4-dim embeddings; query is biased toward rowid 1.
	tbl, err := vec.Create(ctx, db, "docs_vec", 4, vec.Options{
		Metric: vec.Cosine, Encoding: vec.Binary,
	})
	if err != nil {
		log.Fatal(err)
	}
	tbl.BatchInsert(ctx, []vec.Row{
		{Rowid: 1, Embedding: []float32{1.0, 0.0, 0.0, 0.0}},
		{Rowid: 2, Embedding: []float32{0.7, 0.7, 0.0, 0.0}},
		{Rowid: 3, Embedding: []float32{0.0, 1.0, 0.0, 0.0}},
		{Rowid: 4, Embedding: []float32{0.0, 0.0, 1.0, 0.0}},
	})

	// FTS5 side: same rowids, text content. The lexical ranking favors
	// rowid 4 (most occurrences of "fox").
	idx, err := fts.New[int64, string](ctx, db, "docs_fts", fts.Options{
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
	})
	if err != nil {
		log.Fatal(err)
	}
	idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "a fox in the garden"},
		fts.Attr[int64, string]{Key: 2, Value: "two foxes meet"},
		fts.Attr[int64, string]{Key: 3, Value: "the brown dog"},
		fts.Attr[int64, string]{Key: 4, Value: "fox fox fox fox"},
	)

	vecHits, err := tbl.KNNSlice(ctx, []float32{1.0, 0.0, 0.0, 0.0}, 4)
	if err != nil {
		log.Fatal(err)
	}
	ftsHits, err := idx.SearchSlice(ctx, fts.Term("fox"), fts.WithLimit(4))
	if err != nil {
		log.Fatal(err)
	}

	vecKeys := make([]int64, len(vecHits))
	for i, h := range vecHits {
		vecKeys[i] = h.Rowid
	}
	ftsKeys := make([]int64, len(ftsHits))
	for i, h := range ftsHits {
		ftsKeys[i] = h.Key
	}

	fmt.Println("Vec ranking:", vecKeys)
	fmt.Println("FTS ranking:", ftsKeys)

	fused, err := fusion.RRF2(vecKeys, ftsKeys, fusion.WithLimit(4))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\nFused (RRF):")
	for i, r := range fused {
		fmt.Printf("  %d. rowid=%d score=%.5f\n", i+1, r.Key, r.Score)
	}
}
