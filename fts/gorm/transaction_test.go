package ftsgorm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-again/sqlite/fts"
	ftsgorm "github.com/go-again/sqlite/fts/gorm"
	"gorm.io/gorm"
)

// The Go-side sync path (ModeInTable) is where the BLOCKER fix matters:
// external mode is trigger-driven and intrinsically inherits the parent
// transaction, so it can't regress. The in-table model used here is
// declared in mode_test.go.

// TestTransaction_InTable_RollbackCascadesToFTS pins down the BLOCKER
// fix: in-table sync used to reach for db.DB() (the parent *sql.DB)
// and auto-commit even when the surrounding gorm.Transaction rolled
// back. With the fix, the sync goes through db.Statement.ConnPool,
// which is the active *sql.Tx — so a rollback wipes the FTS5 rows.
func TestTransaction_InTable_RollbackCascadesToFTS(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &InTableArticle{}); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("rollback me")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&InTableArticle{Title: "ephemeral", Body: "ephemeral fox"}).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction err=%v, want %v", err, sentinel)
	}

	// Source table reflects rollback.
	var srcN int64
	db.Raw(`select count(*) from in_table_articles`).Scan(&srcN)
	if srcN != 0 {
		t.Errorf("source rows=%d, want 0 after rollback", srcN)
	}

	// FTS5 table must also reflect rollback. Pre-fix this was 1.
	var ftsN int64
	db.Raw(`select count(*) from in_table_articles_fts`).Scan(&ftsN)
	if ftsN != 0 {
		t.Errorf("FTS5 rows=%d, want 0 after rollback (tx bypass regression)", ftsN)
	}

	// And Search agrees.
	results, err := ftsgorm.Search[InTableArticle](
		context.Background(), db, fts.Term("fox"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("Search after rollback: %d hits, want 0", len(results))
	}
}

// TestTransaction_InTable_DeleteRollbackKeepsFTS covers the delete
// path: a rollback after Delete must leave the FTS5 row intact.
func TestTransaction_InTable_DeleteRollbackKeepsFTS(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &InTableArticle{}); err != nil {
		t.Fatal(err)
	}

	a := InTableArticle{Title: "keep", Body: "fox keep"}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("rollback delete")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&a).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction err=%v, want %v", err, sentinel)
	}

	var ftsN int64
	db.Raw(`select count(*) from in_table_articles_fts where rowid = ?`, a.ID).Scan(&ftsN)
	if ftsN != 1 {
		t.Errorf("FTS5 rows for id=%d: %d, want 1 (Delete rollback should keep the row)", a.ID, ftsN)
	}
}

// TestTransaction_Contentless_RollbackCascadesToFTS exercises the
// contentless-mode sync path inside a rolled-back tx. Contentless
// uses the same Go-side syncInsert path as in-table mode, so the
// activePool fix must keep it tx-aware.
func TestTransaction_Contentless_RollbackCascadesToFTS(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &ContentlessArticle{}); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("rollback contentless")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ContentlessArticle{Body: "ephemeral fox"}).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction err=%v, want %v", err, sentinel)
	}

	// Source rolled back.
	var srcN int64
	db.Raw(`select count(*) from contentless_articles`).Scan(&srcN)
	if srcN != 0 {
		t.Errorf("source rows=%d, want 0 after rollback", srcN)
	}

	// FTS5 row should also be gone — Search should find nothing.
	results, err := ftsgorm.Search[ContentlessArticle](
		context.Background(), db, fts.Term("fox"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("Search after contentless rollback: %d hits, want 0", len(results))
	}
}

// TestTransaction_External_RollbackCascadesToFTS documents that
// external-mode rows roll back too, even though the sync is driven by
// SQLite triggers rather than Go-side callbacks. Triggers are
// intrinsically transactional, so this should hold without any of the
// activePool wiring — but pinning a test guards against future
// regressions in how the triggers are installed.
func TestTransaction_External_RollbackCascadesToFTS(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("rollback external")
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&Article{Title: "Ephemeral", Body: "fox"}).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction err=%v, want %v", err, sentinel)
	}

	results, err := ftsgorm.Search[Article](context.Background(), db, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("external-mode Search after rollback: %d hits, want 0", len(results))
	}
}

// TestTransaction_SearchSeesUncommittedWrites pins down the read
// side of the activePool fix: Search now routes through
// db.Statement.ConnPool, so inside a gorm.Transaction it sees the
// tx's own uncommitted writes. Pre-fix it went through *sql.DB and
// would miss them (or deadlock under MaxOpenConns=1).
func TestTransaction_SearchSeesUncommittedWrites(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &InTableArticle{}); err != nil {
		t.Fatal(err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&InTableArticle{Title: "uncommitted", Body: "fox"}).Error; err != nil {
			return err
		}
		results, err := ftsgorm.Search[InTableArticle](
			context.Background(), tx, fts.Term("fox"),
		)
		if err != nil {
			return err
		}
		if len(results) != 1 || results[0].Model.Title != "uncommitted" {
			t.Errorf("Search inside tx: %+v, want 1 'uncommitted' (read-your-own-writes)", results)
		}
		// Roll back via sentinel below.
		return errors.New("rollback")
	})
	if err == nil || err.Error() != "rollback" {
		t.Fatalf("Transaction err=%v, want 'rollback'", err)
	}

	// Outside the tx, the row is gone.
	results, err := ftsgorm.Search[InTableArticle](
		context.Background(), db, fts.Term("fox"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("Search after rollback: %d hits, want 0", len(results))
	}
}
