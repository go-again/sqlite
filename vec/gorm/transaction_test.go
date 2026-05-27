package vecgorm_test

import (
	"errors"
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
