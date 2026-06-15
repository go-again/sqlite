package fts_test

import (
	"context"
	"testing"

	"github.com/go-again/sqlite/fts"
)

func TestIndex_MaintenanceExtras(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	if err := idx.Insert(ctx, fixtureCorpus...); err != nil {
		t.Fatal(err)
	}

	if err := idx.IntegrityCheck(ctx); err != nil {
		t.Errorf("IntegrityCheck on a healthy index: %v", err)
	}
	if err := idx.SetRank(ctx, "bm25(1.0)"); err != nil {
		t.Errorf("SetRank: %v", err)
	}
	// Search still works after configuring the default rank.
	if matches, err := idx.SearchSlice(ctx, fts.Term("brown")); err != nil {
		t.Fatal(err)
	} else if len(matches) == 0 {
		t.Error("search after SetRank returned nothing")
	}
	// DeleteAll is only valid for contentless / external-content tables.
	if err := idx.DeleteAll(ctx); err == nil {
		t.Error("DeleteAll on an ordinary FTS5 table should error")
	}
}

func TestQuery_InitialToken(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	if err := idx.Insert(ctx, fixtureCorpus...); err != nil {
		t.Fatal(err)
	}
	// "^the" matches only the document whose column STARTS with "the" (doc 1:
	// "the quick brown fox …"), not doc 2 ("a brown dog … the moon") where "the"
	// appears mid-text. A plain Term("the") would match both.
	matches, err := idx.SearchSlice(ctx, fts.InitialToken("the"))
	if err != nil {
		t.Fatalf("InitialToken search: %v", err)
	}
	if len(matches) != 1 || matches[0].Key != 1 {
		keys := make([]int64, len(matches))
		for i, m := range matches {
			keys[i] = m.Key
		}
		t.Errorf("InitialToken(the) matched keys %v, want only [1]", keys)
	}
}

func TestQuery_ColumnSetBuild(t *testing.T) {
	got := fts.ColumnSet([]string{"title", "body"}, fts.Term("brown")).Build()
	if want := `{title body} : ("brown")`; got != want {
		t.Errorf("ColumnSet.Build() = %q, want %q", got, want)
	}
	// It also runs against the single-column index without error.
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	if err := idx.Insert(ctx, fixtureCorpus...); err != nil {
		t.Fatal(err)
	}
	matches, err := idx.SearchSlice(ctx, fts.ColumnSet([]string{"value"}, fts.Term("brown")))
	if err != nil {
		t.Fatalf("ColumnSet search: %v", err)
	}
	if len(matches) != 2 {
		t.Errorf("ColumnSet({value}: brown) matched %d, want 2", len(matches))
	}
}
