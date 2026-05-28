package vec_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-again/sqlite/vec"
)

// TestCreate_DefaultFailsOnSecondCall pins the default behavior: two
// Creates of the same table without WithIfNotExists return an error
// that wraps ErrAlreadyExists.
func TestCreate_DefaultFailsOnSecondCall(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := vec.Create(ctx, db, "docs", 4, vec.Options{}); err != nil {
		t.Fatal(err)
	}
	_, err := vec.Create(ctx, db, "docs", 4, vec.Options{})
	if err == nil {
		t.Fatal("second Create with no option: expected error, got nil")
	}
	if !errors.Is(err, vec.ErrAlreadyExists) {
		t.Errorf("err = %v, want errors.Is(err, vec.ErrAlreadyExists)", err)
	}
}

// TestCreate_WithIfNotExists_Succeeds confirms the opt-in path: the
// second Create with WithIfNotExists returns a usable Table handle
// without erroring.
func TestCreate_WithIfNotExists_Succeeds(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	first, err := vec.Create(ctx, db, "docs", 4, vec.Options{},
		vec.WithIfNotExists())
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := first.Insert(ctx, 1, []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}

	second, err := vec.Create(ctx, db, "docs", 4, vec.Options{},
		vec.WithIfNotExists())
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	// The returned handle works against the existing data.
	hits, err := second.KNNSlice(ctx, []float32{1, 0, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Rowid != 1 {
		t.Errorf("KNN after re-Create: %+v, want one hit rowid=1", hits)
	}
}

// TestCreate_WithIfNotExists_DimMismatchUndetected documents the
// trade-off: with WithIfNotExists we do NOT verify that the existing
// table's schema matches the dim / metric / encoding being requested.
// Caller's job to keep those in sync via migrations.
func TestCreate_WithIfNotExists_DimMismatchUndetected(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := vec.Create(ctx, db, "docs", 4, vec.Options{}); err != nil {
		t.Fatal(err)
	}
	// Pretend the table is dim=8 — Create returns no error, because we
	// don't introspect the existing schema. The handle will misbehave
	// at Insert/KNN time when the bound vector length disagrees.
	_, err := vec.Create(ctx, db, "docs", 8, vec.Options{},
		vec.WithIfNotExists())
	if err != nil {
		t.Errorf("dim mismatch under WithIfNotExists: got err %v, want nil (silent by design)", err)
	}
}
