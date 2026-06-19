// vec-keyed: vector search keyed by a string primary key (UUID / slug) instead
// of an int64 rowid, via vec.KeyedTable[string]. The KNN results come back keyed
// by your own string IDs.
//
// Run with:
//
//	just example vec-keyed
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

	// A vec0 table with an explicit `id text primary key` column.
	tbl, err := vec.CreateKeyed[string](ctx, db, "docs", 4, vec.Options{Metric: vec.Cosine})
	if err != nil {
		log.Fatal(err)
	}
	if err := tbl.BatchInsert(ctx, []vec.KeyedRow[string]{
		{Key: "doc-apple", Embedding: []float32{1, 0, 0, 0}},
		{Key: "doc-banana", Embedding: []float32{0.9, 0.1, 0, 0}},
		{Key: "doc-cherry", Embedding: []float32{0, 1, 0, 0}},
		{Key: "doc-date", Embedding: []float32{0, 0, 1, 0}},
	}); err != nil {
		log.Fatal(err)
	}

	fmt.Println("3 nearest to [1,0,0,0] (results keyed by string ID):")
	hits, err := tbl.KNNSlice(ctx, []float32{1, 0, 0, 0}, 3)
	if err != nil {
		log.Fatal(err)
	}
	for _, h := range hits {
		fmt.Printf("  %-12s distance %.4f\n", h.Key, h.Distance)
	}

	// Update and delete address rows by their string key, just like the rowid API.
	if err := tbl.Update(ctx, "doc-cherry", []float32{1, 0, 0, 0}); err != nil {
		log.Fatal(err)
	}
	if err := tbl.Delete(ctx, "doc-date"); err != nil {
		log.Fatal(err)
	}
	hits, _ = tbl.KNNSlice(ctx, []float32{1, 0, 0, 0}, 5)
	fmt.Printf("\nAfter update+delete, %d rows; nearest = %q\n", len(hits), hits[0].Key)
}
