package vecgorm_test

import (
	"context"
	"strings"
	"testing"

	vecgorm "github.com/go-again/sqlite/vec/gorm"
)

// MultiEmbedDoc has two vec-tagged fields. Migrate creates two sidecar
// tables; Create/Delete populate / clean up both. KNN[T] currently
// rejects the model because it can't disambiguate which embedding to
// query — that limitation is exercised below.
type MultiEmbedDoc struct {
	ID    uint `gorm:"primaryKey"`
	Title string
	Text  vecgorm.Embedding `vec:"dim=4;table=multi_embed_docs_text"`
	Image vecgorm.Embedding `vec:"dim=4;table=multi_embed_docs_image"`
}

// TestMultiField_KNNRejected_NoField asserts the explicit error when a
// caller runs KNN against a model that has more than one vec-tagged
// field without picking which one. The error names the available
// fields and points at vecgorm.WithField.
func TestMultiField_KNNRejected_NoField(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &MultiEmbedDoc{}); err != nil {
		t.Fatal(err)
	}
	_, err := vecgorm.KNN[MultiEmbedDoc](context.Background(), db, []float32{1, 0, 0, 0}, 1)
	if err == nil {
		t.Fatal("expected error on multi-field KNN with no WithField, got nil")
	}
	if !strings.Contains(err.Error(), "vecgorm.WithField") {
		t.Errorf("error %q does not point at vecgorm.WithField", err.Error())
	}
	if !strings.Contains(err.Error(), "Text") || !strings.Contains(err.Error(), "Image") {
		t.Errorf("error %q does not name the available fields", err.Error())
	}
}

// TestMultiField_KNN_WithField_PicksField confirms WithField actually
// dispatches to the right sidecar. Insert distinct vectors per field;
// querying with the matching field's exact vector returns its row at
// distance ≈ 0.
func TestMultiField_KNN_WithField_PicksField(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &MultiEmbedDoc{}); err != nil {
		t.Fatal(err)
	}
	doc := MultiEmbedDoc{
		Title: "doc",
		Text:  vecgorm.Embedding{1, 0, 0, 0},
		Image: vecgorm.Embedding{0, 1, 0, 0},
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	textHits, err := vecgorm.KNN[MultiEmbedDoc](ctx, db, []float32{1, 0, 0, 0}, 1,
		vecgorm.WithField("Text"))
	if err != nil {
		t.Fatal(err)
	}
	if len(textHits) != 1 || textHits[0].Distance > 0.0001 {
		t.Errorf("Text KNN: %+v, want one hit at ~0 distance", textHits)
	}

	imageHits, err := vecgorm.KNN[MultiEmbedDoc](ctx, db, []float32{0, 1, 0, 0}, 1,
		vecgorm.WithField("Image"))
	if err != nil {
		t.Fatal(err)
	}
	if len(imageHits) != 1 || imageHits[0].Distance > 0.0001 {
		t.Errorf("Image KNN: %+v, want one hit at ~0 distance", imageHits)
	}
}

// TestMultiField_KNN_WithField_Unknown rejects a typo'd field name
// with an error that names the actual fields.
func TestMultiField_KNN_WithField_Unknown(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &MultiEmbedDoc{}); err != nil {
		t.Fatal(err)
	}
	_, err := vecgorm.KNN[MultiEmbedDoc](context.Background(), db, []float32{1, 0, 0, 0}, 1,
		vecgorm.WithField("Nope"))
	if err == nil {
		t.Fatal("expected error on unknown field name, got nil")
	}
	if !strings.Contains(err.Error(), `"Nope"`) {
		t.Errorf("error %q does not echo the bad field name", err.Error())
	}
	if !strings.Contains(err.Error(), "Text") || !strings.Contains(err.Error(), "Image") {
		t.Errorf("error %q does not list the available fields", err.Error())
	}
}

// TestSingleField_WithField_Accepted confirms that for single-field
// models, passing the field's own name still works — callers who want
// to be explicit shouldn't be punished for it.
func TestSingleField_WithField_Accepted(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	doc := Document{Title: "single", Embedding: []float32{1, 0, 0, 0}}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}
	hits, err := vecgorm.KNN[Document](context.Background(), db, []float32{1, 0, 0, 0}, 1,
		vecgorm.WithField("Embedding"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("WithField on single-field model: %d hits, want 1", len(hits))
	}
}

// TestSingleField_WithField_RejectsWrongName confirms that a typo'd
// field name on a single-field model is rejected loudly, not silently
// ignored. Without this check a caller refactoring a multi-field
// model down to one field could leave a stale WithField("Old") in
// place and silently query the wrong embedding — bad bug to discover
// in prod.
func TestSingleField_WithField_RejectsWrongName(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	_, err := vecgorm.KNN[Document](context.Background(), db, []float32{1, 0, 0, 0}, 1,
		vecgorm.WithField("Nope"))
	if err == nil {
		t.Fatal("expected error on wrong field name (single-field model), got nil")
	}
	if !strings.Contains(err.Error(), `"Nope"`) {
		t.Errorf("error %q does not echo the bad field name", err.Error())
	}
	if !strings.Contains(err.Error(), "Embedding") {
		t.Errorf("error %q does not list the single available field", err.Error())
	}
}

// TestMultiField_CreateDeletePopulatesBothSidecars verifies the write
// side still works on multi-embedding models: Create writes both
// sidecars, Delete cleans both up. The KNN limitation does not block
// CRUD.
func TestMultiField_CreateDeletePopulatesBothSidecars(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &MultiEmbedDoc{}); err != nil {
		t.Fatal(err)
	}
	doc := MultiEmbedDoc{
		Title: "doc",
		Text:  vecgorm.Embedding{1, 0, 0, 0},
		Image: vecgorm.Embedding{0, 1, 0, 0},
	}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}

	var nText, nImage int64
	db.Raw(`select count(*) from multi_embed_docs_text where rowid = ?`, doc.ID).Scan(&nText)
	db.Raw(`select count(*) from multi_embed_docs_image where rowid = ?`, doc.ID).Scan(&nImage)
	if nText != 1 || nImage != 1 {
		t.Errorf("after Create: text=%d image=%d, want 1 each", nText, nImage)
	}

	if err := db.Delete(&doc).Error; err != nil {
		t.Fatal(err)
	}
	db.Raw(`select count(*) from multi_embed_docs_text where rowid = ?`, doc.ID).Scan(&nText)
	db.Raw(`select count(*) from multi_embed_docs_image where rowid = ?`, doc.ID).Scan(&nImage)
	if nText != 0 || nImage != 0 {
		t.Errorf("after Delete: text=%d image=%d, want 0 each", nText, nImage)
	}
}

// TestBatchInsert_SoftDelete exercises insertStmt's soft-delete branch:
// a batch Create on a SoftDoc-shaped table goes through
// batchInsertEmbeddings with m.SoftDelete=true and must populate the
// sidecar with deleted=0. KNN at the matching vector should then find
// every row.
func TestBatchInsert_SoftDelete(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &SoftDoc{}); err != nil {
		t.Fatal(err)
	}
	docs := make([]SoftDoc, 10)
	for i := range docs {
		docs[i] = SoftDoc{Title: "d", Embedding: []float32{float32(i), 0, 0, 0}}
	}
	if err := db.Create(&docs).Error; err != nil {
		t.Fatal(err)
	}

	// Every row should be visible to default KNN (deleted=0).
	var alive int64
	db.Raw(`select count(*) from soft_docs_vec where deleted = 0`).Scan(&alive)
	if alive != int64(len(docs)) {
		t.Errorf("sidecar deleted=0 rows=%d, want %d", alive, len(docs))
	}

	// And no row should have a NULL deleted flag (which would happen
	// if insertStmt forgot the soft-delete branch).
	var nullCount int64
	db.Raw(`select count(*) from soft_docs_vec where deleted is null`).Scan(&nullCount)
	if nullCount != 0 {
		t.Errorf("sidecar rows with deleted=NULL: %d, want 0 (soft-delete insert branch dropped)", nullCount)
	}
}
