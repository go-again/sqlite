// gorm-fts example: side-by-side pattern for combining gorm-managed schema
// with FTS5 full-text search. gorm owns the canonical Articles table; the
// fts.Index lives next to it in external-content mode so it stays compact
// (only the inverted index, no duplicated text). Search returns rowids;
// gorm fetches the full rows.
//
// External-content tables do not auto-sync on writes; this example calls
// Rebuild() after the initial Create batch. For continuous syncing, install
// AFTER INSERT/UPDATE/DELETE triggers per the FTS5 docs section 4.4.3.
package main

import (
	"context"
	"fmt"
	"log"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "gosqlite.org"
	"gosqlite.org/fts"
	sqlitegorm "gosqlite.org/gorm"
)

type Article struct {
	ID    uint `gorm:"primaryKey"`
	Title string
	Body  string
}

func main() {
	ctx := context.Background()

	dsn := "file:gorm-fts-demo?mode=memory&cache=shared"
	gdb, err := gorm.Open(sqlitegorm.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatal(err)
	}
	sqlDB, _ := gdb.DB()

	// 1. gorm migrates the Article model. articles table now exists.
	if err := gdb.AutoMigrate(&Article{}); err != nil {
		log.Fatal(err)
	}

	// 2. Create an FTS5 index in external-content mode pointing at the
	//    gorm-managed articles table. ContentRowid maps to gorm's "id".
	idx, err := fts.New[uint, string](ctx, sqlDB, "articles_fts", fts.Options{
		Columns:   []string{"title", "body"},
		Tokenizer: fts.Porter{Base: fts.Unicode61{RemoveDiacritics: 2}},
		External:  &fts.External{ContentTable: "articles", ContentRowid: "id"},
	})
	if err != nil {
		log.Fatal(err)
	}

	// 3. Insert the rows via gorm.
	articles := []Article{
		{Title: "Foxes", Body: "the quick brown fox jumps over the lazy dog"},
		{Title: "Dogs", Body: "a brown dog barked at the moon"},
		{Title: "Cats", Body: "a cat sat on the mat"},
	}
	if err := gdb.Create(&articles).Error; err != nil {
		log.Fatal(err)
	}

	// 4. External-content FTS5 needs an explicit rebuild after bulk inserts.
	if err := idx.Rebuild(ctx); err != nil {
		log.Fatal(err)
	}

	// 5. Search returns rowid + ranking; we fetch the full rows via gorm.
	matches, err := idx.SearchSlice(ctx,
		fts.Term("brown"),
		fts.WithRanking(),
		fts.WithSnippet("body", "[", "]", "…", 8))
	if err != nil {
		log.Fatal(err)
	}

	ids := make([]uint, len(matches))
	for i, m := range matches {
		ids[i] = m.Key
	}
	var found []Article
	if err := gdb.Where("id IN ?", ids).Find(&found).Error; err != nil {
		log.Fatal(err)
	}
	byID := make(map[uint]Article, len(found))
	for _, a := range found {
		byID[a.ID] = a
	}

	fmt.Println("matches for 'brown':")
	for _, m := range matches {
		a := byID[m.Key]
		fmt.Printf("  id=%d title=%q rank=%.3f snippet=%q\n",
			a.ID, a.Title, m.Rank, m.Snippet)
	}
}
