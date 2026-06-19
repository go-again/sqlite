// vec-search example: typed sqlite-vec API. Blank-importing the vec package
// auto-registers the sqlite-vec extension on every connection; the typed
// vec.Table helpers handle (de)serialization (JSON or binary) for you.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "gosqlite.org"
	"gosqlite.org/vec"
)

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Cosine-similarity, binary encoding for compact storage and fast inserts.
	tbl, err := vec.Create(ctx, db, "docs", 4, vec.Options{
		Metric:   vec.Cosine,
		Encoding: vec.Binary,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Bulk-insert four toy embeddings inside one transaction.
	corpus := []vec.Row{
		{Rowid: 1, Embedding: []float32{1, 0, 0, 0}},
		{Rowid: 2, Embedding: []float32{0.9, 0.1, 0, 0}},
		{Rowid: 3, Embedding: []float32{0, 1, 0, 0}},
		{Rowid: 4, Embedding: []float32{0, 0, 1, 0}},
	}
	if err := tbl.BatchInsert(ctx, corpus); err != nil {
		log.Fatal(err)
	}

	// Streaming KNN. Use KNNSlice if you don't need streaming behavior.
	q := []float32{1, 0.05, 0, 0}
	fmt.Println("top-2 nearest to", q)
	i := 0
	for m, err := range tbl.KNN(ctx, q, 2) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  [%d] rowid=%d distance=%.6f\n", i, m.Rowid, m.Distance)
		i++
	}

	// int8 quantization: 4x smaller storage (vectors must be in the [-1, 1]
	// unit range, which these are). The typed API still works in []float32.
	i8, err := vec.Create(ctx, db, "docs_i8", 4, vec.Options{Encoding: vec.Int8})
	if err != nil {
		log.Fatal(err)
	}
	if err := i8.BatchInsert(ctx, corpus); err != nil {
		log.Fatal(err)
	}
	hits, err := i8.KNNSlice(ctx, q, 1)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nint8-quantized nearest to %v: rowid=%d distance=%.4f\n", q, hits[0].Rowid, hits[0].Distance)

	// Metadata columns: filter the KNN by a non-vector attribute (vec.Metadata),
	// evaluated alongside MATCH.
	meta, err := vec.Create(ctx, db, "docs_meta", 4, vec.Options{
		Columns: []vec.Column{{Name: "lang", Type: "text", Kind: vec.Metadata}},
	})
	if err != nil {
		log.Fatal(err)
	}
	_ = meta.InsertRow(ctx, vec.Row{Rowid: 1, Embedding: []float32{1, 0, 0, 0}, Values: map[string]any{"lang": "en"}})
	_ = meta.InsertRow(ctx, vec.Row{Rowid: 2, Embedding: []float32{1, 0, 0, 0}, Values: map[string]any{"lang": "fr"}})
	enOnly, err := meta.KNNSlice(ctx, []float32{1, 0, 0, 0}, 5, vec.WithFilter("lang = ?", "en"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("metadata-filtered (lang='en'): %d hit(s), rowid=%d\n", len(enOnly), enOnly[0].Rowid)
}
