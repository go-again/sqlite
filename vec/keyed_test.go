package vec_test

import (
	"context"
	"errors"
	"testing"

	"gosqlite.org/vec"
)

func TestKeyed_StringKeys(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.CreateKeyed[string](ctx, db, "items", 4, vec.Options{})
	if err != nil {
		t.Fatalf("CreateKeyed[string]: %v", err)
	}
	if tbl.KeyColumn() != "id" {
		t.Errorf("KeyColumn() = %q, want id", tbl.KeyColumn())
	}
	if err := tbl.BatchInsert(ctx, []vec.KeyedRow[string]{
		{Key: "alpha", Embedding: []float32{1, 0, 0, 0}},
		{Key: "beta", Embedding: []float32{0, 1, 0, 0}},
	}); err != nil {
		t.Fatalf("BatchInsert: %v", err)
	}

	matches, err := tbl.KNNSlice(ctx, []float32{1, 0, 0, 0}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].Key != "alpha" {
		t.Fatalf("KNN = %+v, want alpha first", matches)
	}
	if matches[0].Distance != 0 {
		t.Errorf("self-match distance = %v, want 0", matches[0].Distance)
	}

	// Update moves beta onto the query vector; Delete removes alpha.
	if err := tbl.Update(ctx, "beta", []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := tbl.Delete(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	matches, err = tbl.KNNSlice(ctx, []float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Key != "beta" {
		t.Errorf("after update+delete = %+v, want only beta", matches)
	}
}

func TestKeyed_Int64Keys(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.CreateKeyed[int64](ctx, db, "ints", 4, vec.Options{})
	if err != nil {
		t.Fatalf("CreateKeyed[int64]: %v", err)
	}
	if err := tbl.Insert(ctx, 42, []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	matches, err := tbl.KNNSlice(ctx, []float32{1, 0, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Key != 42 {
		t.Errorf("KNN = %+v, want key 42", matches)
	}
}

func TestKeyed_CustomColumnFilterAndExists(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	tbl, err := vec.CreateKeyed[string](ctx, db, "k", 4, vec.Options{}, vec.WithKeyColumn("uuid"))
	if err != nil {
		t.Fatalf("CreateKeyed with custom key column: %v", err)
	}
	if tbl.KeyColumn() != "uuid" {
		t.Errorf("KeyColumn() = %q, want uuid", tbl.KeyColumn())
	}
	if err := tbl.BatchInsert(ctx, []vec.KeyedRow[string]{
		{Key: "u-1", Embedding: []float32{1, 0, 0, 0}},
		{Key: "u-2", Embedding: []float32{1, 0, 0, 0}},
	}); err != nil {
		t.Fatal(err)
	}
	// Filter on the (custom) key column alongside the MATCH.
	matches, err := tbl.KNNSlice(ctx, []float32{1, 0, 0, 0}, 5, vec.WithFilter("uuid = ?", "u-1"))
	if err != nil {
		t.Fatalf("filtered KNN: %v", err)
	}
	if len(matches) != 1 || matches[0].Key != "u-1" {
		t.Errorf("filtered = %+v, want only u-1", matches)
	}

	// Second create wraps ErrAlreadyExists.
	if _, err := vec.CreateKeyed[string](ctx, db, "k", 4, vec.Options{}, vec.WithKeyColumn("uuid")); !errors.Is(err, vec.ErrAlreadyExists) {
		t.Errorf("second CreateKeyed err = %v, want ErrAlreadyExists", err)
	}
}
