// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package vec

import "fmt"

// Metric identifies the distance function sqlite-vec uses when comparing
// vectors. Cosine and Dot are computed by sqlite-vec; L2 is the squared
// Euclidean distance (matches the "vec_distance_l2sq" SQL helper).
type Metric int

const (
	// L2 is the default; matches sqlite-vec's vec0() default ranking on
	// float[N] columns.
	L2 Metric = iota
	// Cosine selects cosine similarity. Lower distance == more similar.
	Cosine
	// Dot selects negative dot-product (so smaller == more similar, matching
	// the L2/Cosine convention).
	Dot
)

// String renders a metric in the form sqlite-vec's CREATE VIRTUAL TABLE
// option accepts ("L2", "Cosine", "Dot"). Documented here so users who want
// to inspect or log the metric they configured don't have to reach into
// internal helpers.
func (m Metric) String() string {
	switch m {
	case L2:
		return "L2"
	case Cosine:
		return "Cosine"
	case Dot:
		return "Dot"
	}
	return fmt.Sprintf("Metric(%d)", int(m))
}

// Encoding chooses how this package serializes []float32 vectors when sending
// them to SQLite. Both encodings are accepted by sqlite-vec; binary is more
// compact and avoids the JSON parse on every insert, while JSON is human-
// readable and the form used in sqlite-vec's documentation examples.
type Encoding int

const (
	// JSON encodes vectors as the text `[v0, v1, ...]` per sqlite-vec's
	// canonical example syntax. Default for backwards compatibility.
	JSON Encoding = iota
	// Binary encodes vectors as a packed little-endian float32 BLOB and lets
	// sqlite-vec parse it via the vec_f32(?) constructor. Recommended for
	// performance when bulk-inserting.
	Binary
)

// Options configures Create.
type Options struct {
	// Metric selects the distance function used during MATCH queries.
	// Defaults to L2 when zero.
	Metric Metric
	// Encoding selects how Insert/BatchInsert/KNN serialize []float32
	// vectors. Defaults to JSON when zero.
	Encoding Encoding
}
