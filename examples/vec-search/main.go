// vec-search example: typed sqlite-vec API. Blank-importing the vec package
// auto-registers the sqlite-vec extension on every connection; the typed
// vec.Table helpers handle (de)serialization (JSON or binary) for you.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vec"
)

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite3", ":memory:")
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
}
