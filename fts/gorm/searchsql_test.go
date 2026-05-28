package ftsgorm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/go-again/sqlite/fts"
	ftsgorm "github.com/go-again/sqlite/fts/gorm"
)

// TestSearchSQL_Bridge_IncludesSoftDelete_External confirms the bridge's
// external-mode soft-delete filter (deleted_at IS NULL) is auto-
// injected when the model uses gorm.DeletedAt.
func TestSearchSQL_Bridge_IncludesSoftDelete_External(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &SoftArticle{}); err != nil {
		t.Fatal(err)
	}
	sql, _, err := ftsgorm.SearchSQL[SoftArticle](db, fts.Term("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "deleted_at IS NULL") {
		t.Errorf("expected external-mode soft-delete filter, got: %s", sql)
	}
}

// TestSearchSQL_Bridge_IncludeDeletedDropsFilter confirms the
// IncludeDeleted opt-out strips the filter.
func TestSearchSQL_Bridge_IncludeDeletedDropsFilter(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &SoftArticle{}); err != nil {
		t.Fatal(err)
	}
	sql, _, err := ftsgorm.SearchSQL[SoftArticle](db, fts.Term("hello"),
		ftsgorm.IncludeDeleted())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "deleted_at IS NULL") {
		t.Errorf("IncludeDeleted should strip soft-delete filter, got: %s", sql)
	}
}

// TestSearchSQL_Bridge_WithJoinExecutes end-to-end: build SQL with a
// JOIN to the source table, run via db.Raw, verify rows scan.
func TestSearchSQL_Bridge_WithJoinExecutes(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Article{Title: "alpha", Body: "the quick brown fox"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Article{Title: "beta", Body: "no animals here"}).Error; err != nil {
		t.Fatal(err)
	}

	sql, args, err := ftsgorm.SearchSQL[Article](db, fts.Term("fox"),
		ftsgorm.WithSelect("articles.id, articles.title"),
		ftsgorm.WithJoin("JOIN articles ON articles.id = articles_fts.rowid"),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Column layout depends on the projection; we only assert the row
	// count here. A struct-into scan is covered by the corresponding
	// raw-fts tests in fts/searchsql_test.go.
	rows, err := db.Raw(sql, args...).Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for rows.Next() {
		got++
		_ = cols
	}
	if got != 1 {
		t.Errorf("rows=%d, want 1", got)
	}
}

// TestSearch_Bridge_RejectsWithSelect ensures the typed Search[T]
// errors when WithSelect is set; consumers must use SearchSQL.
func TestSearch_Bridge_RejectsWithSelect(t *testing.T) {
	db := openTestDB(t)
	if err := ftsgorm.Migrate(db, &Article{}); err != nil {
		t.Fatal(err)
	}
	_, err := ftsgorm.Search[Article](context.Background(), db, fts.Term("x"),
		ftsgorm.WithSelect("extra"))
	if err == nil {
		t.Fatal("expected error from Search+WithSelect, got nil")
	}
	if !strings.Contains(err.Error(), "WithSelect") {
		t.Errorf("error %q doesn't mention WithSelect", err.Error())
	}
}
