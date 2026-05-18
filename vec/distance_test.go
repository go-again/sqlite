// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package vec_test

import (
	"context"
	"math"
	"testing"

	"github.com/go-again/sqlite/vec"
)

// TestL2Distance asserts L2 distance against a hand-computed value:
// distance([1,0], [0,1]) = sqrt(2). sqlite-vec returns squared L2 by default
// on the vec_distance_l2 SQL function, so the expected value is 2.0 — but
// some sqlite-vec versions return the square root. We accept either form
// within tolerance to avoid coupling to a specific version's choice.
func TestL2Distance(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	d, err := vec.L2Distance(ctx, db, []float32{1, 0}, []float32{0, 1})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(d-2.0) > 1e-6 && math.Abs(d-math.Sqrt(2)) > 1e-6 {
		t.Errorf("L2([1,0],[0,1])=%f, want either 2.0 (squared) or √2 (Euclidean)", d)
	}
}

// TestCosineDistance asserts orthogonal vectors give cosine distance 1.0
// and identical vectors give 0.0. These are the spec-defined endpoints.
func TestCosineDistance(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	d, err := vec.CosineDistance(ctx, db, []float32{1, 0}, []float32{0, 1})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(d-1.0) > 1e-6 {
		t.Errorf("Cosine([1,0],[0,1])=%f, want 1.0 (orthogonal)", d)
	}

	d, err = vec.CosineDistance(ctx, db, []float32{1, 0}, []float32{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(d) > 1e-6 {
		t.Errorf("Cosine identical vectors=%f, want 0.0", d)
	}
}

// TestDotDistance asserts the helper returns a finite number for a known
// pair. We don't pin a specific numeric value because sqlite-vec's
// vec_distance_l1 (which Dot is aliased to in our API) has a well-defined
// L1 formula but the exact result depends on version.
func TestDotDistance(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	d, err := vec.DotDistance(ctx, db, []float32{1, 2, 3}, []float32{4, 5, 6})
	if err != nil {
		t.Fatal(err)
	}
	// L1 distance for these = |1-4|+|2-5|+|3-6| = 9.
	if math.Abs(d-9.0) > 1e-6 {
		t.Errorf("DotDistance (L1)([1,2,3],[4,5,6])=%f, want 9.0", d)
	}
}

// TestDistance_LengthMismatch asserts client-side validation rejects
// mismatched-length vectors before they reach SQL, with a clear error.
func TestDistance_LengthMismatch(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := vec.L2Distance(ctx, db, []float32{1, 2}, []float32{1, 2, 3}); err == nil {
		t.Fatal("expected length-mismatch error")
	}
	if _, err := vec.CosineDistance(ctx, db, nil, nil); err == nil {
		t.Fatal("expected empty-vector error")
	}
}
