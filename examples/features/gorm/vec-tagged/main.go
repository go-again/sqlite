// gorm-vec-tagged: the tag-driven flow for combining gorm models with
// sqlite-vec. Mark an embedding field with `vec:"dim=N"` on a model,
// register the vecgorm.Plugin(), call vecgorm.Migrate, and let
// db.Create / vecgorm.KNN handle the sidecar transparently. No manual
// vec.Table maintenance.
//
// Compare with examples/features/gorm/vec/ for the side-by-side recipe — both
// patterns coexist; pick the one that fits.
package main

import (
	"context"
	"fmt"
	"log"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "gosqlite.org"
	sqlitegorm "gosqlite.org/gorm"
	vecgorm "gosqlite.org/vec/gorm"
)

// Document is a typical gorm model with one tagged embedding field.
// The vecgorm.Embedding wrapper type lets us skip the gorm:"-" tag —
// the wrapper implements gorm's GormDataType interface so the schema
// parser accepts it; the plugin then sets IgnoreMigration=true on the
// field so no BLOB column lands on the source table.
type Document struct {
	ID        uint `gorm:"primaryKey"`
	Title     string
	Body      string
	Embedding vecgorm.Embedding `vec:"dim=4;metric=cosine"`
}

// Article uses a string primary key (a slug) instead of an int64 ID. The
// tag-driven sidecar supports both — it detects the PK type at registration and
// keys the sidecar on an `id text primary key` column, no extra tag required.
type Article struct {
	Slug      string `gorm:"primaryKey"`
	Title     string
	Embedding vecgorm.Embedding `vec:"dim=4;metric=cosine"`
}

func main() {
	db, err := gorm.Open(sqlitegorm.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := db.Use(vecgorm.Plugin()); err != nil {
		log.Fatal(err)
	}
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		log.Fatal(err)
	}

	// Seed three documents. Embeddings populate the sidecar transparently.
	docs := []Document{
		{Title: "north", Body: "polar bears", Embedding: vecgorm.Embedding{0, 1, 0, 0}},
		{Title: "east", Body: "sunrises", Embedding: vecgorm.Embedding{1, 0, 0, 0}},
		{Title: "south", Body: "deserts", Embedding: vecgorm.Embedding{0, -1, 0, 0}},
	}
	if err := db.Create(&docs).Error; err != nil {
		log.Fatal(err)
	}

	// Find the closest document to a query vector. KNN returns
	// []Hit[Document] with .Distance attached — no manual IN-clause
	// or re-sorting required.
	ctx := context.Background()
	results, err := vecgorm.KNN[Document](ctx, db, []float32{0, 0.95, 0, 0}, 2)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("KNN results:")
	for _, r := range results {
		fmt.Printf("  id=%d title=%q distance=%.4f\n", r.Model.ID, r.Model.Title, r.Distance)
	}

	// The same flow works for a string-PK model — no code changes beyond the
	// model's primary-key type.
	if err := vecgorm.Migrate(db, &Article{}); err != nil {
		log.Fatal(err)
	}
	if err := db.Create(&[]Article{
		{Slug: "bears-of-the-north", Title: "Bears", Embedding: vecgorm.Embedding{0, 1, 0, 0}},
		{Slug: "eastern-sunrises", Title: "Sunrises", Embedding: vecgorm.Embedding{1, 0, 0, 0}},
	}).Error; err != nil {
		log.Fatal(err)
	}
	arts, err := vecgorm.KNN[Article](ctx, db, []float32{1, 0, 0, 0}, 1)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("string-PK KNN: nearest article slug=%q distance=%.4f\n", arts[0].Model.Slug, arts[0].Distance)

	// Cleanup is automatic via gorm's Migrator. db.Migrator().DropTable
	// also drops the sidecar through the plugin's DropTableHook.
	if err := db.Migrator().DropTable(&Document{}); err != nil {
		log.Fatal(err)
	}
	fmt.Println("DropTable cascade: source + sidecar both gone")
}
