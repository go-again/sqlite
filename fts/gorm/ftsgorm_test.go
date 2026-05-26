package ftsgorm_test

import (
	"context"
	"strings"
	"testing"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/fts"
	ftsgorm "github.com/go-again/sqlite/fts/gorm"
	sqlitegorm "github.com/go-again/sqlite/gorm"

	"gorm.io/gorm"
)

type Article struct {
	ID    uint   `gorm:"primaryKey"`
	Title string `fts5:"tokenize=porter+unicode61"`
	Body  string `fts5:"tokenize=porter+unicode61"`
}

// testDialector returns a dialector for the helper-less plugin test
// case in lifecycle_test.go.
func testDialector() gorm.Dialector {
	return sqlitegorm.Open(":memory:")
}

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
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.Use(ftsgorm.Plugin()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPlugin_RegisterIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := db.Use(ftsgorm.Plugin()); err == nil {
		t.Fatal("expected error on double Use")
	}
}

func TestMigrate_CreatesFTSTableAndTriggers(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var n int64
	db.Raw(`select count(*) from sqlite_master where type='table' and name='articles_fts'`).Scan(&n)
	if n != 1 {
		t.Errorf("articles_fts: %d, want 1", n)
	}
	db.Raw(`select count(*) from sqlite_master where type='trigger' and name like 'ftsgorm_articles_%'`).Scan(&n)
	if n != 3 {
		t.Errorf("triggers count=%d, want 3 (ai/au/ad)", n)
	}
}

func TestCreate_IndexedByTrigger(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Article{Title: "Hello world", Body: "The quick brown fox"}).Error; err != nil {
		t.Fatal(err)
	}
	results, err := ftsgorm.Search[Article](context.Background(), db, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d, want 1", len(results))
	}
	if results[0].Model.Title != "Hello world" {
		t.Errorf("title=%q, want 'Hello world'", results[0].Model.Title)
	}
}

func TestUpdate_RefreshesIndex(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	a := Article{Title: "old title", Body: "old body"}
	db.Create(&a)
	a.Body = "the fox jumped"
	if err := db.Save(&a).Error; err != nil {
		t.Fatal(err)
	}
	results, err := ftsgorm.Search[Article](context.Background(), db, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("after Update: results=%d, want 1", len(results))
	}
}

func TestDelete_RemovesFromIndex(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	a := Article{Title: "fox", Body: "ran"}
	db.Create(&a)
	if err := db.Delete(&a).Error; err != nil {
		t.Fatal(err)
	}
	results, _ := ftsgorm.Search[Article](context.Background(), db, fts.Term("fox"))
	if len(results) != 0 {
		t.Errorf("after Delete: results=%d, want 0", len(results))
	}
}

func TestSearch_WithSnippetAndHighlight(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&Article{Title: "Quick", Body: "The quick brown fox jumps over the lazy dog"})

	results, err := ftsgorm.Search[Article](
		context.Background(), db, fts.Term("fox"),
		ftsgorm.WithSnippet("body", "<b>", "</b>", "…", 5),
		ftsgorm.WithHighlight("body", "[", "]"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%d, want 1", len(results))
	}
	if !strings.Contains(results[0].Snippet, "<b>fox</b>") {
		t.Errorf("snippet=%q missing <b>fox</b>", results[0].Snippet)
	}
	if !strings.Contains(results[0].Highlight, "[fox]") {
		t.Errorf("highlight=%q missing [fox]", results[0].Highlight)
	}
}

func TestSearch_RankingWeights(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&Article{Title: "fox", Body: "tail mention"})
	db.Create(&Article{Title: "tail", Body: "fox is here"})

	// Title-weighted ranking: title="fox" wins.
	results, err := ftsgorm.Search[Article](context.Background(), db, fts.Term("fox"),
		ftsgorm.WithRanking(10, 0.1))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 1 || results[0].Model.Title != "fox" {
		t.Errorf("title-weighted: top=%v, want title=fox", results[0].Model.Title)
	}

	// Body-weighted ranking: body="fox is here" wins.
	results, err = ftsgorm.Search[Article](context.Background(), db, fts.Term("fox"),
		ftsgorm.WithRanking(0.1, 10))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 1 || results[0].Model.Title != "tail" {
		t.Errorf("body-weighted: top=%v, want title=tail", results[0].Model.Title)
	}
}

// SoftArticle uses gorm.DeletedAt to enable soft-delete handling.
type SoftArticle struct {
	ID        uint   `gorm:"primaryKey"`
	Title     string `fts5:"tokenize=porter+unicode61"`
	DeletedAt gorm.DeletedAt
}

func TestSoftDelete_ExcludedFromSearch(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &SoftArticle{}); err != nil {
		t.Fatal(err)
	}
	alive := SoftArticle{Title: "fox alive"}
	dead := SoftArticle{Title: "fox dead"}
	db.Create(&alive)
	db.Create(&dead)
	if err := db.Delete(&dead).Error; err != nil {
		t.Fatal(err)
	}

	// Default: dead excluded.
	results, err := ftsgorm.Search[SoftArticle](context.Background(), db, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Model.Title != "fox alive" {
		t.Errorf("default search: %+v, want only 'fox alive'", results)
	}

	// IncludeDeleted: both surface.
	results, err = ftsgorm.Search[SoftArticle](
		context.Background(), db.Unscoped(), fts.Term("fox"),
		ftsgorm.IncludeDeleted(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("IncludeDeleted: results=%d, want 2", len(results))
	}
}

func TestMigrate_BackfillsExistingRows(t *testing.T) {
	db := openTestDB(t)
	// Create the source table first, insert rows, THEN migrate (creating
	// the FTS5 table + triggers). The Migrate call should backfill so
	// pre-existing rows are searchable.
	if err := db.AutoMigrate(&Article{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&Article{Title: "pre", Body: "fox"})
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	results, err := ftsgorm.Search[Article](context.Background(), db, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("backfill: results=%d, want 1", len(results))
	}
}

func TestDropSidecar_RemovesTriggersAndTable(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	if err := ftsgorm.DropSidecar(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Raw(`select count(*) from sqlite_master where name='articles_fts' or name like 'ftsgorm_articles_%'`).Scan(&n)
	if n != 0 {
		t.Errorf("after DropSidecar: residual rows=%d, want 0", n)
	}
}
