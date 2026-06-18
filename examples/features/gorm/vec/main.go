// gorm-vec example: side-by-side pattern for combining gorm-managed schema
// with sqlite-vec virtual tables. gorm owns the canonical Documents table
// (typed model, AutoMigrate, normal CRUD); the vec.Table holds embeddings
// keyed by Document.ID. KNN returns rowids, gorm fetches the matching
// documents.
//
// This is the recommended pattern when "gorm-vec integration" comes up —
// no AutoMigrate magic, no virtual-table gymnastics, just shared rowids.
package main

import (
	"context"
	"fmt"
	"log"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "github.com/go-again/sqlite"
	sqlite "github.com/go-again/sqlite/gorm"
	"github.com/go-again/sqlite/vec"
)

// Document is a typical gorm model. The ID is what the vec.Table's rowid
// column references — keep them in lockstep on insert.
type Document struct {
	ID    uint `gorm:"primaryKey"`
	Title string
	Body  string
}

func main() {
	ctx := context.Background()

	// 1. Open gorm normally. Use mode=memory + cache=shared so the gorm
	//    pool and the *sql.DB we hand to vec.Create see the same database.
	dsn := "file:gorm-vec-demo?mode=memory&cache=shared"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatal(err)
	}
	sqlDB, _ := gdb.DB()

	// 2. gorm migrates the Document model.
	if err := gdb.AutoMigrate(&Document{}); err != nil {
		log.Fatal(err)
	}

	// 3. vec.Create issues CREATE VIRTUAL TABLE; the typed Table handle is
	//    independent of gorm.
	tbl, err := vec.Create(ctx, sqlDB, "doc_vecs", 4, vec.Options{
		Metric: vec.Cosine,
	})
	if err != nil {
		log.Fatal(err)
	}

	// 4. Insert documents and matching embeddings using the gorm-allocated
	//    primary key as the vec rowid.
	docs := []*Document{
		{Title: "fox", Body: "the quick brown fox"},
		{Title: "dog", Body: "lazy dog basks in the sun"},
		{Title: "moon", Body: "moon over the mountain"},
	}
	embeddings := [][]float32{
		{1, 0, 0, 0},
		{0.9, 0.1, 0, 0},
		{0, 0, 1, 0},
	}
	for i, d := range docs {
		if err := gdb.Create(d).Error; err != nil {
			log.Fatal(err)
		}
		if err := tbl.Insert(ctx, int64(d.ID), embeddings[i]); err != nil {
			log.Fatal(err)
		}
	}

	// 5. KNN search → rowids → gorm fetches the rest of the columns.
	matches, err := tbl.KNNSlice(ctx, []float32{1, 0.05, 0, 0}, 2)
	if err != nil {
		log.Fatal(err)
	}

	ids := make([]int64, len(matches))
	for i, m := range matches {
		ids[i] = m.Rowid
	}

	var found []Document
	if err := gdb.Where("id IN ?", ids).Find(&found).Error; err != nil {
		log.Fatal(err)
	}

	// Reorder found by the vec ranking (gorm returns in id order).
	rank := map[uint]int{}
	for i, m := range matches {
		rank[uint(m.Rowid)] = i
	}
	ordered := make([]Document, len(found))
	for _, d := range found {
		ordered[rank[d.ID]] = d
	}

	fmt.Println("top-2 nearest documents:")
	for i, d := range ordered {
		fmt.Printf("  [%d] id=%d title=%q (distance=%.6f)\n",
			i, d.ID, d.Title, matches[i].Distance)
	}
}
