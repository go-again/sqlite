// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package vec

import (
	"context"
	"database/sql"
	"fmt"
)

// L2Distance returns the L2 (Euclidean) distance between two vectors using
// sqlite-vec's vec_distance_l2 SQL function. The vectors must be the same
// length; we round-trip through SQL so callers get exactly the same value
// the vec0 virtual table would compute internally — useful for spot-checking
// fixtures or scoring known pairs without inserting them into a table.
//
// The supplied db can be any *sql.DB that has loaded the sqlite-vec
// extension (any *sql.DB obtained from this package's parent driver after
// `import _ "github.com/go-again/sqlite/vec"` qualifies).
func L2Distance(ctx context.Context, db *sql.DB, a, b []float32) (float64, error) {
	return distance(ctx, db, "vec_distance_l2", a, b)
}

// CosineDistance returns the cosine distance via vec_distance_cosine.
func CosineDistance(ctx context.Context, db *sql.DB, a, b []float32) (float64, error) {
	return distance(ctx, db, "vec_distance_cosine", a, b)
}

// DotDistance returns sqlite-vec's negative-dot-product distance.
// sqlite-vec exposes this as the L1 distance fn but calls the metric "dot"
// in the vec0 options; we expose the SQL fn here under DotDistance for
// symmetry with the Metric constants.
func DotDistance(ctx context.Context, db *sql.DB, a, b []float32) (float64, error) {
	return distance(ctx, db, "vec_distance_l1", a, b)
}

func distance(ctx context.Context, db *sql.DB, fn string, a, b []float32) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vec.%s: length mismatch (a=%d, b=%d)", fn, len(a), len(b))
	}
	if len(a) == 0 {
		return 0, fmt.Errorf("vec.%s: empty vectors", fn)
	}
	// Use binary encoding (vec_f32) on both sides — same precision regardless
	// of the table's storage encoding, and avoids re-parsing the JSON form.
	stmt := "SELECT " + fn + "(vec_f32(?), vec_f32(?))"
	var d float64
	if err := db.QueryRowContext(ctx, stmt, encodeBinary(a), encodeBinary(b)).Scan(&d); err != nil {
		return 0, fmt.Errorf("vec.%s: %w", fn, err)
	}
	return d, nil
}
