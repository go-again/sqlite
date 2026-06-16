package vecgorm_test

import (
	"context"
	"testing"

	vecgorm "github.com/go-again/sqlite/vec/gorm"
	"gorm.io/gorm"
)

// SoftStringDoc is a string-PK model with gorm soft-delete.
type SoftStringDoc struct {
	ID        string `gorm:"primaryKey"`
	Title     string
	DeletedAt gorm.DeletedAt `gorm:"index"`
	Embedding []float32      `gorm:"-" vec:"dim=4;table=softstringdocs_vec"`
}

// StringKeyDoc has a string primary key, which the tag-driven sidecar does not
// yet support.
type StringKeyDoc struct {
	ID        string `gorm:"primaryKey"`
	Title     string
	Embedding []float32 `gorm:"-" vec:"dim=4;table=stringkeydocs_vec"`
}

// TestStringPK_WritePath: a string-PK model migrates to a text-keyed sidecar
// and Create writes the embeddings keyed by the string PK.
func TestStringPK_WritePath(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &StringKeyDoc{}); err != nil {
		t.Fatalf("Migrate string-PK model: %v", err)
	}
	for _, d := range []StringKeyDoc{
		{ID: "uuid-1", Title: "a", Embedding: []float32{1, 0, 0, 0}},
		{ID: "uuid-2", Title: "b", Embedding: []float32{0, 1, 0, 0}},
	} {
		if err := db.Create(&d).Error; err != nil {
			t.Fatalf("create %s: %v", d.ID, err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := sqlDB.QueryRow(`SELECT count(*) FROM stringkeydocs_vec`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("sidecar row count = %d, want 2", n)
	}
	var id string
	if err := sqlDB.QueryRow(`SELECT id FROM stringkeydocs_vec ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != "uuid-1" {
		t.Errorf("sidecar key column id = %q, want uuid-1 (string PK stored)", id)
	}
}

// TestStringPK_KNN: typed KNN over a string-PK model returns the models keyed by
// their string PK, in rank order.
func TestStringPK_KNN(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := vecgorm.Migrate(db, &StringKeyDoc{}); err != nil {
		t.Fatal(err)
	}
	docs := []StringKeyDoc{
		{ID: "alpha", Embedding: []float32{1, 0, 0, 0}},
		{ID: "beta", Embedding: []float32{0, 1, 0, 0}},
		{ID: "gamma", Embedding: []float32{0, 0, 1, 0}},
	}
	for i := range docs {
		if err := db.Create(&docs[i]).Error; err != nil {
			t.Fatalf("create %s: %v", docs[i].ID, err)
		}
	}
	hits, err := vecgorm.KNN[StringKeyDoc](ctx, db, []float32{1, 0, 0, 0}, 2)
	if err != nil {
		t.Fatalf("KNN: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].Model.ID != "alpha" {
		t.Errorf("nearest = %q, want alpha", hits[0].Model.ID)
	}
	if hits[0].Distance != 0 {
		t.Errorf("self-match distance = %v, want 0", hits[0].Distance)
	}
}

// TestStringPK_UpdateDelete: Save and Delete keep a string-PK sidecar in sync.
func TestStringPK_UpdateDelete(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := vecgorm.Migrate(db, &StringKeyDoc{}); err != nil {
		t.Fatal(err)
	}
	a := StringKeyDoc{ID: "a", Embedding: []float32{1, 0, 0, 0}}
	b := StringKeyDoc{ID: "b", Embedding: []float32{0, 1, 0, 0}}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}

	// Move b onto the query vector via Save, then hard-delete a.
	b.Embedding = []float32{1, 0, 0, 0}
	if err := db.Save(&b).Error; err != nil {
		t.Fatalf("save b: %v", err)
	}
	if err := db.Delete(&a).Error; err != nil {
		t.Fatalf("delete a: %v", err)
	}

	hits, err := vecgorm.KNN[StringKeyDoc](ctx, db, []float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Model.ID != "b" {
		t.Errorf("after update+delete, hits = %+v, want only b", hits)
	}
}

// TestStringPK_SoftDelete: soft-deleting a string-PK model flips the sidecar's
// deleted flag (via the key-column join), so KNN excludes it by default.
func TestStringPK_SoftDelete(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := vecgorm.Migrate(db, &SoftStringDoc{}); err != nil {
		t.Fatal(err)
	}
	a := SoftStringDoc{ID: "a", Embedding: []float32{1, 0, 0, 0}}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	if hits, err := vecgorm.KNN[SoftStringDoc](ctx, db, []float32{1, 0, 0, 0}, 5); err != nil {
		t.Fatal(err)
	} else if len(hits) != 1 {
		t.Fatalf("before delete: hits = %d, want 1", len(hits))
	}

	if err := db.Delete(&a).Error; err != nil { // soft delete
		t.Fatalf("soft delete: %v", err)
	}
	hits, err := vecgorm.KNN[SoftStringDoc](ctx, db, []float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("after soft-delete: hits = %d, want 0 (excluded by deleted flag)", len(hits))
	}
}
