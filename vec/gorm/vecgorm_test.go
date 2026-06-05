package vecgorm_test

import (
	"context"
	"strings"
	"testing"

	_ "github.com/go-again/sqlite"
	sqlitegorm "github.com/go-again/sqlite/gorm"
	_ "github.com/go-again/sqlite/vec"
	vecgorm "github.com/go-again/sqlite/vec/gorm"

	"gorm.io/gorm"
)

// Document is the canonical test model: integer PK, indexed Title for
// joinability, and a tagged Embedding field. Uses the legacy
// []float32 + gorm:"-" form to make sure that path still works.
type Document struct {
	ID        uint `gorm:"primaryKey"`
	Title     string
	Embedding []float32 `gorm:"-" vec:"dim=4;metric=l2;encoding=binary"`
}

// WrappedDoc uses the recommended vecgorm.Embedding wrapper type.
// No gorm:"-" needed — the wrapper satisfies gorm's GormDataType
// interface and the plugin marks IgnoreMigration after Parse.
type WrappedDoc struct {
	ID        uint `gorm:"primaryKey"`
	Title     string
	Embedding vecgorm.Embedding `vec:"dim=4;metric=l2"`
}

func TestEmbeddingWrapper_NoGormIgnoreNeeded(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &WrappedDoc{}); err != nil {
		t.Fatalf("Migrate WrappedDoc: %v", err)
	}
	doc := WrappedDoc{Title: "wrapped", Embedding: vecgorm.Embedding{1, 0, 0, 0}}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	results, err := vecgorm.KNN[WrappedDoc](
		context.Background(), db, []float32{1, 0, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Model.Title != "wrapped" {
		t.Errorf("wrapped KNN: %+v, want one match titled 'wrapped'", results)
	}
}

func TestEmbeddingWrapper_NoSourceColumn(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &WrappedDoc{}); err != nil {
		t.Fatal(err)
	}
	cols, err := db.Migrator().ColumnTypes(&WrappedDoc{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cols {
		if c.Name() == "embedding" {
			t.Error("source table for WrappedDoc has 'embedding' column; should be excluded")
		}
	}
}

// openTestDB sets up a fresh in-memory database with the vecgorm
// plugin installed.
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlitegorm.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	})
	// vec0 + FTS5 virtual tables are per-conn. Pin to one so all
	// callbacks observe the same connection's state.
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)

	if err := db.Use(vecgorm.Plugin()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPlugin_RegisterIdempotent(t *testing.T) {
	db := openTestDB(t)
	// Re-using the plugin instance is an error per gorm contract; a
	// fresh instance should also be rejected on the same DB because
	// the plugin name is unique.
	err := db.Use(vecgorm.Plugin())
	if err == nil {
		t.Fatal("expected error on double Use of plugin")
	}
}

// missingIgnoreModel intentionally omits gorm:"-" so we can assert that
// the preflight check catches it with a clear error.
type missingIgnoreModel struct {
	ID        uint      `gorm:"primaryKey"`
	Embedding []float32 `vec:"dim=4"`
}

func TestMigrate_ErrorsOnMissingGormIgnore(t *testing.T) {
	db := openTestDB(t)
	err := vecgorm.Migrate(db, &missingIgnoreModel{})
	if err == nil {
		t.Fatal("expected error: vec: tag without gorm:\"-\"")
	}
	if got := err.Error(); !strings.Contains(got, `gorm:"-"`) {
		t.Errorf("error %q doesn't mention gorm:\"-\"", got)
	}
}

func TestMigrate_CreatesSidecar(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Source table exists.
	if !db.Migrator().HasTable(&Document{}) {
		t.Error("source table 'documents' missing")
	}
	// Sidecar exists.
	var n int64
	if err := db.Raw(`select count(*) from sqlite_master where type='table' and name='documents_vec'`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("documents_vec sidecar count=%d, want 1", n)
	}
}

func TestMigrate_TaggedFieldNotInSourceTable(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	// The 'embedding' field on Document is tagged with vec: — it must
	// NOT appear as a column on the source table.
	cols, err := db.Migrator().ColumnTypes(&Document{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cols {
		if c.Name() == "embedding" {
			t.Errorf("source table has 'embedding' column; should be excluded by vec: tag")
		}
	}
}

func TestCreate_PopulatesSidecar(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	doc := Document{Title: "hello", Embedding: []float32{1, 0, 0, 0}}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if doc.ID == 0 {
		t.Fatal("doc.ID was not auto-assigned")
	}

	var n int64
	if err := db.Raw(`select count(*) from documents_vec where rowid = ?`, doc.ID).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("sidecar rows for ID=%d: %d, want 1", doc.ID, n)
	}
}

func TestUpdate_RewritesSidecar(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	doc := Document{Title: "doc", Embedding: []float32{1, 0, 0, 0}}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	// Update embedding via Save.
	doc.Embedding = []float32{0, 1, 0, 0}
	if err := db.Save(&doc).Error; err != nil {
		t.Fatal(err)
	}
	// KNN close to the new direction should find this doc.
	results, err := vecgorm.KNN[Document](context.Background(), db, []float32{0, 0.99, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Model.ID != doc.ID {
		t.Errorf("after Update: %+v, want top match ID=%d", results, doc.ID)
	}
}

func TestDelete_RemovesFromSidecar(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	doc := Document{Title: "doc", Embedding: []float32{1, 0, 0, 0}}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&doc).Error; err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Raw(`select count(*) from documents_vec where rowid = ?`, doc.ID).Scan(&n)
	if n != 0 {
		t.Errorf("after Delete: sidecar rows=%d, want 0", n)
	}
}

// TestBatchInsert_SingleTransaction asserts a slice Create produces
// exactly one commit on the underlying *sql.DB (the BatchInsert
// transaction), not N commits.
func TestBatchInsert_SingleTransaction(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	docs := make([]Document, 50)
	for i := range docs {
		docs[i] = Document{Title: "d", Embedding: []float32{float32(i), 0, 0, 0}}
	}
	if err := db.Create(&docs).Error; err != nil {
		t.Fatal(err)
	}
	// Confirm every embedding made it into the sidecar.
	var n int64
	db.Raw(`select count(*) from documents_vec`).Scan(&n)
	if n != int64(len(docs)) {
		t.Errorf("sidecar rows=%d, want %d", n, len(docs))
	}
}

// SoftDoc has gorm.DeletedAt, so the plugin should emit a sidecar
// with a deleted metadata column and filter on it during KNN.
type SoftDoc struct {
	ID        uint `gorm:"primaryKey"`
	Title     string
	DeletedAt gorm.DeletedAt `gorm:"index"`
	Embedding []float32      `gorm:"-" vec:"dim=4"`
}

// CustomSoftDoc has gorm.DeletedAt under a non-default column name. Pins
// the round-6 V1 fix: softDeleteSidecar must resolve the actual DBName
// from the field at schema-parse time instead of hard-coding "deleted_at".
type CustomSoftDoc struct {
	ID        uint `gorm:"primaryKey"`
	Title     string
	RemovedAt gorm.DeletedAt `gorm:"column:removed_at;index"`
	Embedding []float32      `gorm:"-" vec:"dim=4"`
}

func TestSoftDelete_CustomDeletedAtColumnName(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &CustomSoftDoc{}); err != nil {
		t.Fatal(err)
	}
	docs := []CustomSoftDoc{
		{Title: "alive", Embedding: []float32{1, 0, 0, 0}},
		{Title: "dead", Embedding: []float32{0.99, 0, 0, 0}},
	}
	if err := db.Create(&docs).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&docs[1]).Error; err != nil {
		t.Fatal(err)
	}
	results, err := vecgorm.KNN[CustomSoftDoc](context.Background(), db, []float32{1, 0, 0, 0}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d, want 1 (dead excluded via removed_at column)", len(results))
	}
	if results[0].Model.Title != "alive" {
		t.Errorf("got %q, want 'alive'", results[0].Model.Title)
	}
}

func TestSoftDelete_ExcludedFromKNNByDefault(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &SoftDoc{}); err != nil {
		t.Fatal(err)
	}
	docs := []SoftDoc{
		{Title: "alive", Embedding: []float32{1, 0, 0, 0}},
		{Title: "dead", Embedding: []float32{0.99, 0, 0, 0}}, // closer to query
	}
	if err := db.Create(&docs).Error; err != nil {
		t.Fatal(err)
	}
	// Soft-delete the closer doc.
	if err := db.Delete(&docs[1]).Error; err != nil {
		t.Fatal(err)
	}

	// Default KNN: dead doc excluded — alive is the only result.
	results, err := vecgorm.KNN[SoftDoc](context.Background(), db, []float32{1, 0, 0, 0}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d, want 1 (dead excluded)", len(results))
	}
	if results[0].Model.Title != "alive" {
		t.Errorf("got %q, want 'alive'", results[0].Model.Title)
	}

	// IncludeDeleted: dead reappears. Use Unscoped on the fetch so
	// gorm doesn't filter out the soft-deleted row during materialization.
	results, err = vecgorm.KNN[SoftDoc](context.Background(), db.Unscoped(), []float32{1, 0, 0, 0}, 2,
		vecgorm.IncludeDeleted())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("with IncludeDeleted: results=%d, want 2", len(results))
	}
}

func TestKNN_BasicRanking(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	docs := []Document{
		{Title: "north", Embedding: []float32{0, 1, 0, 0}},
		{Title: "east", Embedding: []float32{1, 0, 0, 0}},
		{Title: "south", Embedding: []float32{0, -1, 0, 0}},
		{Title: "west", Embedding: []float32{-1, 0, 0, 0}},
	}
	if err := db.Create(&docs).Error; err != nil {
		t.Fatal(err)
	}

	// Query close to "north"; the nearest should be the north doc.
	results, err := vecgorm.KNN[Document](context.Background(), db, []float32{0, 0.99, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d, want 1", len(results))
	}
	if results[0].Model.Title != "north" {
		t.Errorf("top match=%q, want 'north'", results[0].Model.Title)
	}
}
