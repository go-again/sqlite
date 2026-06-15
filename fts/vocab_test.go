package fts_test

import (
	"context"
	"testing"

	"github.com/go-again/sqlite/fts"
)

func TestVocab_RowAndTop(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	if err := idx.Insert(ctx, fixtureCorpus...); err != nil {
		t.Fatal(err)
	}

	v, err := fts.NewVocab(ctx, db, "docs", fts.VocabRow)
	if err != nil {
		t.Fatalf("NewVocab(row): %v", err)
	}
	terms, err := v.Terms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTerm := map[string]fts.VocabTerm{}
	for _, tm := range terms {
		byTerm[tm.Term] = tm
	}
	// "brown" appears in docs 1 and 2 → 2 documents.
	if got := byTerm["brown"]; got.Documents != 2 {
		t.Errorf("brown documents = %d, want 2", got.Documents)
	}
	// "the": doc 1 twice + doc 2 once → 2 documents, 3 occurrences.
	if got := byTerm["the"]; got.Documents != 2 || got.Occurrences != 3 {
		t.Errorf("the = {docs %d, occ %d}, want {2, 3}", got.Documents, got.Occurrences)
	}
	// Ordered by descending occurrence count.
	for i := 1; i < len(terms); i++ {
		if terms[i-1].Occurrences < terms[i].Occurrences {
			t.Errorf("Terms not ordered by descending occurrences at %d", i)
			break
		}
	}
	top, err := v.TopTerms(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 3 {
		t.Errorf("TopTerms(3) returned %d, want 3", len(top))
	}
}

func TestVocab_ColAndInstance(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	idx := newIdx(t, db, fts.Options{})
	if err := idx.Insert(ctx, fixtureCorpus...); err != nil {
		t.Fatal(err)
	}

	vc, err := fts.NewVocab(ctx, db, "docs", fts.VocabCol)
	if err != nil {
		t.Fatal(err)
	}
	colTerms, err := vc.Terms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(colTerms) == 0 {
		t.Fatal("col vocab returned no terms")
	}
	for _, tm := range colTerms {
		if tm.Column != "value" {
			t.Errorf("col vocab Column = %q, want the single column %q", tm.Column, "value")
			break
		}
	}

	vi, err := fts.NewVocab(ctx, db, "docs", fts.VocabInstance)
	if err != nil {
		t.Fatal(err)
	}
	insts, err := vi.Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(insts) == 0 {
		t.Fatal("instance vocab returned no occurrences")
	}
	found := false
	for _, in := range insts {
		if in.Term == "brown" && in.Column == "value" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a 'brown' occurrence in the instance vocab")
	}

	// Cross-kind calls are rejected.
	if _, err := vi.Terms(ctx); err == nil {
		t.Error("Terms on an instance vocab should error")
	}
	if _, err := vc.Instances(ctx); err == nil {
		t.Error("Instances on a col vocab should error")
	}
}

func TestVocab_IdempotentAndDrop(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	newIdx(t, db, fts.Options{})

	if _, err := fts.NewVocab(ctx, db, "docs", fts.VocabRow); err != nil {
		t.Fatal(err)
	}
	// Second create without the idempotent option wraps ErrVocabAlreadyExists.
	if _, err := fts.NewVocab(ctx, db, "docs", fts.VocabRow); err == nil {
		t.Error("second NewVocab should error without WithVocabIfNotExists")
	}
	// With the option it is a no-op.
	v, err := fts.NewVocab(ctx, db, "docs", fts.VocabRow, fts.WithVocabIfNotExists())
	if err != nil {
		t.Fatalf("idempotent NewVocab: %v", err)
	}
	if err := v.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	// After Drop a fresh create succeeds.
	if _, err := fts.NewVocab(ctx, db, "docs", fts.VocabRow); err != nil {
		t.Errorf("NewVocab after Drop: %v", err)
	}
}
