// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package sqlite

import (
	"context"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vec"
)

// docModel is a typical gorm-managed model; the vec sidecar table is keyed
// by docModel.ID.
type docModel struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

func (docModel) TableName() string { return "docs" }

// TestGormVec_SideBySide is the canonical "use vec with gorm" recipe: gorm
// owns the schema, vec.Table holds the embeddings, the gorm primary key is
// the vec rowid. Search returns rowids; gorm fetches the rows.
//
// This is the pattern that should appear in the README. The test exists so
// any future change that breaks this composition (e.g. a vec build tag that
// also affects gorm) is caught.
func TestGormVec_SideBySide(t *testing.T) {
	ctx := context.Background()
	dsn := "file:gorm-vec-test?mode=memory&cache=shared"
	gdb, err := gorm.Open(Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := gdb.DB()
	t.Cleanup(func() { sqlDB.Close() })

	if err := gdb.AutoMigrate(&docModel{}); err != nil {
		t.Fatal(err)
	}

	tbl, err := vec.Create(ctx, sqlDB, "doc_vecs", 4, vec.Options{Metric: vec.Cosine})
	if err != nil {
		t.Fatal(err)
	}

	docs := []docModel{
		{Title: "fox"},
		{Title: "dog"},
		{Title: "moon"},
	}
	embeddings := [][]float32{
		{1, 0, 0, 0},
		{0.9, 0.1, 0, 0},
		{0, 0, 1, 0},
	}
	for i := range docs {
		if err := gdb.Create(&docs[i]).Error; err != nil {
			t.Fatal(err)
		}
		if err := tbl.Insert(ctx, int64(docs[i].ID), embeddings[i]); err != nil {
			t.Fatal(err)
		}
	}

	matches, err := tbl.KNNSlice(ctx, []float32{1, 0.05, 0, 0}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("KNN matches=%d, want 2", len(matches))
	}

	ids := []int64{matches[0].Rowid, matches[1].Rowid}
	var got []docModel
	if err := gdb.Where("id IN ?", ids).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Find matched %d rows, want 2", len(got))
	}

	// The top match for [1, 0.05, 0, 0] cosine-distance-wise is docs[0]
	// ("fox", [1,0,0,0]); docs[1] is second; docs[2] is far away.
	titles := map[uint]string{}
	for _, d := range got {
		titles[d.ID] = d.Title
	}
	topTitle := titles[uint(matches[0].Rowid)]
	if topTitle != "fox" {
		t.Errorf("top match title=%q, want fox", topTitle)
	}
}
