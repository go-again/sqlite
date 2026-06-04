package vec

import (
	"fmt"
	"strings"
)

// Metric identifies the distance function sqlite-vec uses when comparing
// vectors. The three sqlite-vec-supported metrics are L1, L2, and Cosine;
// see https://alexgarcia.xyz/sqlite-vec/api-reference.html for the
// authoritative list.
type Metric int

const (
	// L2 is the default; squared Euclidean distance. Matches sqlite-vec's
	// vec0() default ranking on float[N] columns.
	L2 Metric = iota
	// Cosine selects cosine distance. Range [0, 2]; smaller is more similar.
	Cosine
	// Dot maps to sqlite-vec's L1 (Manhattan / taxicab) distance — the name
	// is kept for plan/historical compatibility but the metric is L1.
	// Smaller is more similar. Callers wanting a true inner-product score
	// can compute it client-side from the raw vectors.
	Dot
)

// String renders a metric for logging and inspection. The strings are
// approximate human labels, not the keywords sqlite-vec accepts in the
// vec0 constructor — see Keyword for that.
func (m Metric) String() string {
	switch m {
	case L2:
		return "L2"
	case Cosine:
		return "Cosine"
	case Dot:
		return "Dot(L1)"
	}
	return fmt.Sprintf("Metric(%d)", int(m))
}

// Keyword returns the sqlite-vec vec0() constructor keyword for this
// metric: "l2", "cosine", or "l1" (for the Dot alias). Use this when
// building CREATE VIRTUAL TABLE statements outside vec.Create.
func (m Metric) Keyword() string {
	switch m {
	case Cosine:
		return "cosine"
	case Dot:
		return "l1"
	}
	// L2 / unknown
	return "l2"
}

// ParseMetric maps a case-insensitive keyword to a Metric. Recognised
// values: "l2" (default), "cosine", "dot", "l1". Useful when reading
// metric names from tags, configs, or user input.
func ParseMetric(s string) (Metric, error) {
	switch strings.ToLower(s) {
	case "", "l2":
		return L2, nil
	case "cosine":
		return Cosine, nil
	case "dot", "l1":
		return Dot, nil
	}
	return 0, fmt.Errorf("vec: unknown metric %q (want l2 | cosine | dot)", s)
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

// ParseEncoding maps a case-insensitive keyword to an Encoding.
// Recognised values: "json" (default), "binary".
func ParseEncoding(s string) (Encoding, error) {
	switch strings.ToLower(s) {
	case "", "json":
		return JSON, nil
	case "binary":
		return Binary, nil
	}
	return 0, fmt.Errorf("vec: unknown encoding %q (want json | binary)", s)
}

// Options configures Create.
type Options struct {
	// Metric selects the distance function used during MATCH queries.
	// Defaults to L2 when zero.
	Metric Metric
	// Encoding selects how Insert/BatchInsert/KNN serialize []float32
	// vectors. Defaults to JSON when zero.
	Encoding Encoding
}
