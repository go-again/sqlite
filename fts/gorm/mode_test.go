package ftsgorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/go-again/sqlite/fts"
	ftsgorm "github.com/go-again/sqlite/fts/gorm"

	"gorm.io/gorm"
)

// InTableArticle uses external=false → FTS5 stores the indexed text
// itself, no trigger machinery.
type InTableArticle struct {
	ID    uint   `gorm:"primaryKey"`
	Title string `fts5:"tokenize=porter+unicode61;external=false"`
	Body  string `fts5:"tokenize=porter+unicode61"`
}

func TestMode_InTable_NoTriggers(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &InTableArticle{}); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Raw(`select count(*) from sqlite_master where type='trigger' and name like 'ftsgorm_in_table_articles_%'`).Scan(&n)
	if n != 0 {
		t.Errorf("in-table mode should not install triggers, got %d", n)
	}
}

func TestMode_InTable_RowCallbackWrites(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &InTableArticle{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&InTableArticle{Title: "Hello", Body: "The quick brown fox"}).Error; err != nil {
		t.Fatal(err)
	}
	results, err := ftsgorm.Search[InTableArticle](context.Background(), db, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("in-table search: results=%d, want 1", len(results))
	}
}

func TestMode_InTable_UpdateDelete(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &InTableArticle{}); err != nil {
		t.Fatal(err)
	}
	a := InTableArticle{Title: "before", Body: "old"}
	db.Create(&a)
	a.Body = "fox in the new body"
	if err := db.Save(&a).Error; err != nil {
		t.Fatal(err)
	}
	results, _ := ftsgorm.Search[InTableArticle](context.Background(), db, fts.Term("fox"))
	if len(results) != 1 {
		t.Errorf("after Update: results=%d, want 1", len(results))
	}
	if err := db.Delete(&a).Error; err != nil {
		t.Fatal(err)
	}
	results, _ = ftsgorm.Search[InTableArticle](context.Background(), db, fts.Term("fox"))
	if len(results) != 0 {
		t.Errorf("after Delete: results=%d, want 0", len(results))
	}
}

// ContentlessArticle: content=” — only the inverted index, no text.
type ContentlessArticle struct {
	ID   uint   `gorm:"primaryKey"`
	Body string `fts5:"tokenize=porter+unicode61;contentless=true"`
}

func TestMode_Contentless_SnippetRejected(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &ContentlessArticle{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&ContentlessArticle{Body: "the quick brown fox"})

	_, err := ftsgorm.Search[ContentlessArticle](
		context.Background(), db, fts.Term("fox"),
		ftsgorm.WithSnippet("body", "<b>", "</b>", "…", 5),
	)
	if err == nil {
		t.Fatal("expected error: snippet on contentless")
	}
	if !strings.Contains(err.Error(), "contentless mode") {
		t.Errorf("error %q doesn't mention contentless mode", err.Error())
	}
}

func TestMode_Contentless_SearchWorks(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &ContentlessArticle{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&ContentlessArticle{Body: "the quick brown fox"})

	results, err := ftsgorm.Search[ContentlessArticle](context.Background(), db, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("contentless search: results=%d, want 1", len(results))
	}
}

// Conflicting: external=true AND contentless=true on the same model.
type conflictingMode struct {
	ID    uint   `gorm:"primaryKey"`
	Title string `fts5:"external=true"`
	Body  string `fts5:"contentless=true"`
}

func TestMode_ConflictingTags(t *testing.T) {
	db := openTestDB(t)
	err := ftsgorm.Migrate(db, &conflictingMode{})
	if err == nil {
		t.Fatal("expected error: external+contentless conflict")
	}
	if !strings.Contains(err.Error(), "conflicting") {
		t.Errorf("error %q doesn't mention conflict", err.Error())
	}
}

// SoftInTableArticle exercises the soft-delete path under ModeInTable.
type SoftInTableArticle struct {
	ID        uint   `gorm:"primaryKey"`
	Title     string `fts5:"external=false"`
	DeletedAt gorm.DeletedAt
}

func TestMode_InTable_SoftDelete(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &SoftInTableArticle{}); err != nil {
		t.Fatal(err)
	}
	alive := SoftInTableArticle{Title: "fox alive"}
	dead := SoftInTableArticle{Title: "fox dead"}
	db.Create(&alive)
	db.Create(&dead)
	db.Delete(&dead)

	results, _ := ftsgorm.Search[SoftInTableArticle](context.Background(), db, fts.Term("fox"))
	if len(results) != 1 || results[0].Model.Title != "fox alive" {
		t.Errorf("in-table soft-delete: %+v, want only 'fox alive'", results)
	}
}
