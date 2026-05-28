package fts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-again/sqlite/fts"
)

// TestNew_DefaultFailsOnSecondCall pins the default behavior: two
// calls to New for the same table without WithIfNotExists return an
// error wrapping ErrAlreadyExists.
func TestNew_DefaultFailsOnSecondCall(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := fts.New[int64, string](ctx, db, "docs", fts.Options{}); err != nil {
		t.Fatal(err)
	}
	_, err := fts.New[int64, string](ctx, db, "docs", fts.Options{})
	if err == nil {
		t.Fatal("second New with no option: expected error, got nil")
	}
	if !errors.Is(err, fts.ErrAlreadyExists) {
		t.Errorf("err = %v, want errors.Is(err, fts.ErrAlreadyExists)", err)
	}
}

// TestNew_WithIfNotExists_Succeeds confirms the opt-in path: the
// second New with WithIfNotExists returns a usable Index handle
// without erroring.
func TestNew_WithIfNotExists_Succeeds(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	first, err := fts.New[int64, string](ctx, db, "docs", fts.Options{},
		fts.WithIfNotExists())
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	if err := first.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "the quick brown fox"}); err != nil {
		t.Fatal(err)
	}

	second, err := fts.New[int64, string](ctx, db, "docs", fts.Options{},
		fts.WithIfNotExists())
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	hits, err := second.SearchSlice(ctx, fts.Term("fox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Key != 1 {
		t.Errorf("Search after re-New: %+v, want one hit key=1", hits)
	}
}
