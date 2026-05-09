// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package fts_test

import (
	"context"
	"testing"

	"github.com/go-again/sqlite/fts"
)

// TestExternal_ContentTable verifies the external-content / contentless mode
// of FTS5. We create a regular table, an FTS5 index referencing it via the
// content= option, populate the source, then Rebuild() to populate the index.
//
// FTS5 docs say external-content indexes don't auto-sync on source updates,
// so explicit Rebuild is part of the workflow.
//
// See https://www.sqlite.org/fts5.html section 4.4.
func TestExternal_ContentTable(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
CREATE TABLE articles (
    id    INTEGER PRIMARY KEY,
    title TEXT
);
INSERT INTO articles (id, title) VALUES (1, 'fox and dog'), (2, 'cat alone');
`); err != nil {
		t.Fatal(err)
	}

	idx, err := fts.New[int64, string](ctx, db, "articles_fts", fts.Options{
		Columns:  []string{"title"},
		External: &fts.External{ContentTable: "articles", ContentRowid: "id"},
	})
	if err != nil {
		t.Fatalf("New external: %v", err)
	}

	// External-content tables don't auto-populate; Rebuild fills the index
	// from the source.
	if err := idx.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	matches, err := idx.SearchSlice(ctx, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Key != 1 {
		t.Errorf("Term('fox') matches=%+v, want [{Key:1}]", matches)
	}
}
