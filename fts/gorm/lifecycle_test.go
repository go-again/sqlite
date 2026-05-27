package ftsgorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/go-again/sqlite/fts"
	ftsgorm "github.com/go-again/sqlite/fts/gorm"

	"gorm.io/gorm"
)

// conflictingTokenize declares two fields with different tokenize
// options on the same model — must be rejected at parse time.
type conflictingTokenize struct {
	ID    uint   `gorm:"primaryKey"`
	Title string `fts5:"tokenize=ascii"`
	Body  string `fts5:"tokenize=porter"`
}

func TestTagParse_RejectsConflictingTokenize(t *testing.T) {
	db := openTestDB(t)
	err := ftsgorm.Migrate(db, &conflictingTokenize{})
	if err == nil {
		t.Fatal("expected error on conflicting tokenize")
	}
	if !strings.Contains(err.Error(), "conflicting") {
		t.Errorf("error %q doesn't mention conflict", err.Error())
	}
}

// nonStringField asserts fts5: on a non-string field errors.
type nonStringField struct {
	ID    uint `gorm:"primaryKey"`
	Count int  `fts5:""`
}

func TestTagParse_RejectsNonStringField(t *testing.T) {
	db := openTestDB(t)
	err := ftsgorm.Migrate(db, &nonStringField{})
	if err == nil {
		t.Fatal("expected error on non-string fts5: field")
	}
	if !strings.Contains(err.Error(), "only apply to string") {
		t.Errorf("error %q doesn't mention string-only constraint", err.Error())
	}
}

// noTagFTSModel: Migrate is a no-op for models without fts5: tags.
type noTagFTSModel struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

func TestMigrate_NoOpForModelsWithoutTags(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &noTagFTSModel{}); err != nil {
		t.Fatalf("Migrate no-op should succeed: %v", err)
	}
	// No FTS5 table created.
	var n int64
	db.Raw(`select count(*) from sqlite_master where type='table' and name like '%_fts'`).Scan(&n)
	if n != 0 {
		t.Errorf("unexpected FTS5 tables created: %d", n)
	}
}

// compositePKFTS: composite PK should be rejected.
type compositePKFTS struct {
	A     uint   `gorm:"primaryKey"`
	B     uint   `gorm:"primaryKey"`
	Title string `fts5:"tokenize=ascii"`
}

func TestMigrate_RejectsCompositePK(t *testing.T) {
	db := openTestDB(t)
	err := ftsgorm.Migrate(db, &compositePKFTS{})
	if err == nil {
		t.Fatal("expected error on composite PK")
	}
	if !strings.Contains(err.Error(), "primary-key") {
		t.Errorf("error %q doesn't mention primary-key", err.Error())
	}
}

func TestSearch_PreservesOrder(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	// Three docs with different relevance for "fox".
	db.Create(&Article{Title: "fox fox fox", Body: ""})       // most relevant
	db.Create(&Article{Title: "fox", Body: ""})               // medium
	db.Create(&Article{Title: "the lazy dog", Body: "a fox"}) // least

	results, err := ftsgorm.Search[Article](context.Background(), db, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results=%d, want 3", len(results))
	}
	// FTS5 ranks lower = better. Results returned in ORDER BY rank
	// ASC; the assertion just checks monotonic non-decreasing rank.
	for i := 1; i < len(results); i++ {
		if results[i].Rank < results[i-1].Rank {
			t.Errorf("rank not monotonic at i=%d: %v", i, results)
		}
	}
}

func TestSearch_WithLimitOffset(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		db.Create(&Article{Title: "fox " + string(rune('A'+i)), Body: ""})
	}
	results, err := ftsgorm.Search[Article](
		context.Background(), db, fts.Term("fox"),
		ftsgorm.WithLimit(2), ftsgorm.WithOffset(1),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("LIMIT 2 OFFSET 1: %d, want 2", len(results))
	}
}

// TestDropTable_CascadesToSidecarAndTriggers asserts the gorm
// dialector's DropTable hook fires our DropSidecar, taking the FTS5
// table and all three triggers down without explicit cleanup.
func TestDropTable_CascadesToSidecarAndTriggers(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&Article{}); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Raw(`select count(*) from sqlite_master where name='articles_fts' or name like 'ftsgorm_articles_%'`).Scan(&n)
	if n != 0 {
		t.Errorf("after DropTable cascade: residual rows=%d, want 0", n)
	}
}

// noPluginDB calls Migrate without first installing the plugin —
// should error clearly.
func TestMigrate_ErrorsWithoutPlugin(t *testing.T) {
	db, err := gorm.Open(testDialector(), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()
	if err := ftsgorm.Migrate(db, &Article{}); err == nil {
		t.Fatal("expected error when Plugin() not installed")
	}
}
