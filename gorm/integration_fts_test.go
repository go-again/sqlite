// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package sqlite

import (
	"context"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/fts"
)

// articleModel is a typical gorm model; the FTS5 sidecar references it via
// external-content mode so the FTS table only stores the inverted index.
type articleModel struct {
	ID    uint `gorm:"primaryKey"`
	Title string
	Body  string
}

func (articleModel) TableName() string { return "articles" }

// TestGormFTS_ExternalContent is the canonical "use fts with gorm" recipe:
// gorm owns the canonical articles table, the FTS5 index is external-content
// pointing at it, Rebuild() syncs after bulk inserts, Search returns row
// keys + ranking + snippet, gorm fetches the full rows.
func TestGormFTS_ExternalContent(t *testing.T) {
	ctx := context.Background()
	dsn := "file:gorm-fts-test?mode=memory&cache=shared"
	gdb, err := gorm.Open(Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := gdb.DB()
	t.Cleanup(func() { sqlDB.Close() })

	if err := gdb.AutoMigrate(&articleModel{}); err != nil {
		t.Fatal(err)
	}

	idx, err := fts.New[uint, string](ctx, sqlDB, "articles_fts", fts.Options{
		Columns:   []string{"title", "body"},
		Tokenizer: fts.Porter{Base: fts.Unicode61{RemoveDiacritics: 2}},
		External:  &fts.External{ContentTable: "articles", ContentRowid: "id"},
	})
	if err != nil {
		t.Fatal(err)
	}

	rows := []articleModel{
		{Title: "Foxes", Body: "the quick brown fox jumps over the lazy dog"},
		{Title: "Dogs", Body: "a brown dog barked at the moon"},
		{Title: "Cats", Body: "a cat sat on the mat"},
	}
	if err := gdb.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	// External-content tables don't auto-sync — Rebuild populates the index
	// from the source table.
	if err := idx.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}

	matches, err := idx.SearchSlice(ctx, fts.Term("brown"),
		fts.WithRanking(),
		fts.WithSnippet("body", "[", "]", "…", 8))
	if err != nil {
		t.Fatal(err)
	}

	keys := make([]uint, len(matches))
	for i, m := range matches {
		keys[i] = m.Key
	}
	// Both Foxes (id 1) and Dogs (id 2) mention "brown"; Cats (id 3) doesn't.
	if !slices.Contains(keys, uint(1)) || !slices.Contains(keys, uint(2)) {
		t.Errorf("matches missing one of {1,2}: keys=%v", keys)
	}
	if slices.Contains(keys, uint(3)) {
		t.Errorf("matches contain id 3 (Cats), should not: %v", keys)
	}

	// Fetch the gorm rows for the returned keys and confirm snippet wrapping.
	var got []articleModel
	if err := gdb.Where("id IN ?", keys).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != len(matches) {
		t.Errorf("gorm Find returned %d, want %d", len(got), len(matches))
	}
	if !strings.Contains(matches[0].Snippet, "[brown]") {
		t.Errorf("snippet=%q does not wrap 'brown' with brackets", matches[0].Snippet)
	}
}
