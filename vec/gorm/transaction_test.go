package vecgorm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	vecgorm "github.com/go-again/sqlite/vec/gorm"
	"gorm.io/gorm"
)

// TestTransaction_RollbackCascadesToSidecar pins down the BLOCKER fix:
// sidecar writes used to reach for db.DB() (the parent *sql.DB) and
// auto-commit even when the surrounding gorm.Transaction rolled back.
// With the fix, they go through db.Statement.ConnPool, which is the
// active *sql.Tx — so a rollback wipes the sidecar rows too.
func TestTransaction_RollbackCascadesToSidecar(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("rollback me")
	err := db.Transaction(func(tx *gorm.DB) error {
		doc := Document{Title: "ephemeral", Embedding: []float32{1, 0, 0, 0}}
		if err := tx.Create(&doc).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction err=%v, want %v", err, sentinel)
	}

	// Source table reflects rollback.
	var srcN int64
	db.Raw(`select count(*) from documents`).Scan(&srcN)
	if srcN != 0 {
		t.Errorf("source rows=%d, want 0 after rollback", srcN)
	}

	// Sidecar must also reflect rollback. Pre-fix this was 1.
	var sideN int64
	db.Raw(`select count(*) from documents_vec`).Scan(&sideN)
	if sideN != 0 {
		t.Errorf("sidecar rows=%d, want 0 after rollback (tx bypass regression)", sideN)
	}
}

// TestTransaction_BatchRollbackCascadesToSidecar covers the batch
// insert path through batchInsertEmbeddings: when the parent tx rolls
// back, no row from the slice should survive in the sidecar.
func TestTransaction_BatchRollbackCascadesToSidecar(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("rollback batch")
	err := db.Transaction(func(tx *gorm.DB) error {
		docs := make([]Document, 20)
		for i := range docs {
			docs[i] = Document{Title: "d", Embedding: []float32{float32(i), 0, 0, 0}}
		}
		if err := tx.Create(&docs).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction err=%v, want %v", err, sentinel)
	}

	var sideN int64
	db.Raw(`select count(*) from documents_vec`).Scan(&sideN)
	if sideN != 0 {
		t.Errorf("sidecar rows=%d, want 0 after batch rollback", sideN)
	}
}

// TestTransaction_DeleteRollbackKeepsSidecar covers the delete path:
// if the surrounding tx rolls back, sidecar rows the callback would
// have deleted must remain.
func TestTransaction_DeleteRollbackKeepsSidecar(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}

	doc := Document{Title: "keep", Embedding: []float32{1, 0, 0, 0}}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("rollback delete")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&doc).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction err=%v, want %v", err, sentinel)
	}

	var sideN int64
	db.Raw(`select count(*) from documents_vec where rowid = ?`, doc.ID).Scan(&sideN)
	if sideN != 1 {
		t.Errorf("sidecar rows=%d, want 1 (Delete rollback should keep the sidecar row)", sideN)
	}
}

// TestTransaction_UpdateRollbackKeepsSidecar exercises updateEmbedding
// inside a rolled-back tx: the sidecar's stored vector must revert to
// the value it held before the tx started.
func TestTransaction_UpdateRollbackKeepsSidecar(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}

	doc := Document{Title: "orig", Embedding: []float32{1, 0, 0, 0}}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("rollback update")
	err := db.Transaction(func(tx *gorm.DB) error {
		doc.Embedding = []float32{0, 1, 0, 0}
		if err := tx.Save(&doc).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction err=%v, want %v", err, sentinel)
	}

	// Sidecar's stored embedding should still match the pre-tx value.
	// A KNN at the original embedding must return the doc with distance 0.
	results, err := vecgorm.KNN[Document](context.Background(), db, []float32{1, 0, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Distance > 0.0001 {
		t.Errorf("after rollback KNN at original vector: %+v, want distance≈0 (Update bypassed rollback)", results)
	}
}

// TestTransaction_SoftDeleteRollbackKeepsAlive uses the SoftDoc model
// to exercise the soft-delete codepath (softDeleteSidecar through
// db.Exec) inside a rolled-back tx. The sidecar's deleted flag should
// stay at 0 — i.e. the soft-deleted row should remain visible to KNN.
func TestTransaction_SoftDeleteRollbackKeepsAlive(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &SoftDoc{}); err != nil {
		t.Fatal(err)
	}

	doc := SoftDoc{Title: "alive", Embedding: []float32{1, 0, 0, 0}}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("rollback soft delete")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&doc).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction err=%v, want %v", err, sentinel)
	}

	// Source row still present (gorm.DeletedAt is nil).
	var srcN int64
	db.Raw(`select count(*) from soft_docs where deleted_at is null`).Scan(&srcN)
	if srcN != 1 {
		t.Errorf("source alive rows=%d, want 1 after soft-delete rollback", srcN)
	}

	// Sidecar deleted flag must be 0; default KNN should find it.
	results, err := vecgorm.KNN[SoftDoc](context.Background(), db, []float32{1, 0, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Model.Title != "alive" {
		t.Errorf("after soft-delete rollback KNN: %+v, want 1 result titled 'alive'", results)
	}
}

// KNN inside an active gorm.Transaction is intentionally not covered
// here: vec/gorm.KNN reads through *sql.DB (via openSidecar/poolDB)
// so it sees the latest committed state, not the tx's uncommitted
// writes. Combined with the openTestDB fixture's MaxOpenConns=1
// (required to keep vec0/FTS5 virtual tables on one connection), KNN
// inside a tx deadlocks waiting for the only conn the parent tx is
// holding. Callers wanting KNN-in-tx semantics should issue raw SQL
// through tx.Raw / tx.Exec instead. See knn.go for the documented
// contract.

// TestUpdateEmbedding_DimensionMismatch exercises the size check on
// updateEmbedding's input. The check is in the helper, not in
// vec.Table.Update — verify it fires through the gorm callback path.
func TestUpdateEmbedding_DimensionMismatch(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	doc := Document{Title: "doc", Embedding: []float32{1, 0, 0, 0}}
	if err := db.Create(&doc).Error; err != nil {
		t.Fatal(err)
	}

	// Save with a 3-element embedding against a dim=4 sidecar must error.
	doc.Embedding = []float32{1, 2, 3}
	err := db.Save(&doc).Error
	if err == nil {
		t.Fatal("expected error on dim mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "embedding length 3 != dim 4") {
		t.Errorf("error %q does not mention the dim mismatch", err.Error())
	}
}
