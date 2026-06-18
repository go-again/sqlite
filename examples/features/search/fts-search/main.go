// fts-search example: typed FTS5 API with Porter stemming, BM25 ranking,
// and snippet/highlight extraction.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/fts"
)

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Tokenizer: fts.Porter{Base: fts.Unicode61{RemoveDiacritics: 2}},
	})
	if err != nil {
		log.Fatal(err)
	}

	err = idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "The quick brown fox jumps over the lazy dog."},
		fts.Attr[int64, string]{Key: 2, Value: "A brown dog barked at the moon."},
		fts.Attr[int64, string]{Key: 3, Value: "Pack my box with five dozen liquor jugs."},
		fts.Attr[int64, string]{Key: 4, Value: "Running rivers run through ridges."},
	)
	if err != nil {
		log.Fatal(err)
	}

	// "run" should stem-match "running" via Porter.
	matches, err := idx.SearchSlice(ctx, fts.Term("run"),
		fts.WithRanking(),
		fts.WithSnippet("value", "[", "]", "…", 8))
	if err != nil {
		log.Fatal(err)
	}
	for _, m := range matches {
		fmt.Printf("rowid=%d rank=%.3f snippet=%q\n", m.Key, m.Rank, m.Snippet)
	}
}
