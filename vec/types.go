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
	// Hamming is the bit-vector distance: the number of differing bits. It is
	// the only metric sqlite-vec supports for bit[N] columns (the [Bit]
	// encoding), where it is the implicit default.
	Hamming
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
	case Hamming:
		return "Hamming"
	}
	return fmt.Sprintf("Metric(%d)", int(m))
}

// Keyword returns the sqlite-vec vec0() constructor keyword for this
// metric: "l2", "cosine", "l1" (for the Dot alias), or "hamming". Use this
// when building CREATE VIRTUAL TABLE statements outside vec.Create.
func (m Metric) Keyword() string {
	switch m {
	case Cosine:
		return "cosine"
	case Dot:
		return "l1"
	case Hamming:
		return "hamming"
	}
	// L2 / unknown
	return "l2"
}

// ParseMetric maps a case-insensitive keyword to a Metric. Recognised
// values: "l2" (default), "cosine", "dot", "l1", "hamming". Useful when
// reading metric names from tags, configs, or user input.
func ParseMetric(s string) (Metric, error) {
	switch strings.ToLower(s) {
	case "", "l2":
		return L2, nil
	case "cosine":
		return Cosine, nil
	case "dot", "l1":
		return Dot, nil
	case "hamming":
		return Hamming, nil
	}
	return 0, fmt.Errorf("vec: unknown metric %q (want l2 | cosine | dot | hamming)", s)
}

// Encoding chooses how this package serializes []float32 vectors when sending
// them to SQLite. Both encodings are accepted by sqlite-vec; binary is more
// compact and avoids the JSON parse on every insert, while JSON is human-
// readable and the form used in sqlite-vec's documentation examples.
type Encoding int

const (
	// JSON encodes vectors as the text `[v0, v1, ...]` per sqlite-vec's
	// canonical example syntax. Default for backwards compatibility. Column
	// type float[N].
	JSON Encoding = iota
	// Binary encodes vectors as a packed little-endian float32 BLOB and lets
	// sqlite-vec parse it via the vec_f32(?) constructor. Recommended for
	// performance when bulk-inserting. Column type float[N].
	Binary
	// Int8 stores vectors as a quantized int8[N] column — 4x smaller than
	// float32. The caller still works in []float32; sqlite-vec quantizes at
	// insert/query time via vec_quantize_int8(?, 'unit'), which assumes each
	// component is in [-1, 1] (L2-normalize first, or scale into range).
	Int8
	// Bit stores vectors as a packed bit[N] column — 32x smaller than float32,
	// one bit per dimension (sign). The caller works in []float32; sqlite-vec
	// quantizes via vec_quantize_binary(?). Bit columns rank by [Hamming]
	// distance only; Create forces it.
	Bit
)

// columnType is the vec0 column storage type this encoding declares: float for
// JSON/Binary, int8 for Int8, bit for Bit.
func (e Encoding) columnType() string {
	switch e {
	case Int8:
		return "int8"
	case Bit:
		return "bit"
	default:
		return "float"
	}
}

// ParseEncoding maps a case-insensitive keyword to an Encoding.
// Recognised values: "json" (default), "binary", "int8", "bit".
func ParseEncoding(s string) (Encoding, error) {
	switch strings.ToLower(s) {
	case "", "json":
		return JSON, nil
	case "binary":
		return Binary, nil
	case "int8":
		return Int8, nil
	case "bit":
		return Bit, nil
	}
	return 0, fmt.Errorf("vec: unknown encoding %q (want json | binary | int8 | bit)", s)
}

// ColumnKind classifies a non-embedding vec0 column.
type ColumnKind int

const (
	// Metadata is an indexed, filterable column: a KNN query may constrain it
	// with a WHERE predicate (via [WithFilter]) evaluated alongside MATCH.
	Metadata ColumnKind = iota
	// Partition declares a partition-key column: sqlite-vec shards the index by
	// its value so a query filtered to one partition skips the others. Also
	// filterable like Metadata.
	Partition
	// Auxiliary is an unindexed stored payload (vec0's `+col`): returned with a
	// row but not usable in a KNN WHERE predicate. Cheaper than Metadata when
	// you only need to read the value back, not filter on it.
	Auxiliary
)

// Column declares a non-embedding column on a vec0 table — sqlite-vec's
// mechanism for filtered, partitioned, and payload-carrying vector search.
type Column struct {
	// Name is the column identifier (must satisfy ValidIdent).
	Name string
	// Type is the SQL storage type: "text", "integer", "float", or "blob".
	Type string
	// Kind selects metadata (filterable), partition-key, or auxiliary (payload).
	Kind ColumnKind
}

// Options configures Create.
type Options struct {
	// Metric selects the distance function used during MATCH queries.
	// Defaults to L2 when zero.
	Metric Metric
	// Encoding selects how Insert/BatchInsert/KNN serialize []float32
	// vectors. Defaults to JSON when zero.
	Encoding Encoding
	// Columns declares non-embedding metadata / partition-key / auxiliary
	// columns. Their values are carried per row by [Row.Values]; metadata and
	// partition columns are filterable in KNN via [WithFilter]; auxiliary and
	// metadata columns are retrievable via [WithSelect] + [Table.KNNSQL].
	Columns []Column
	// ChunkSize sets vec0's chunk_size= (vectors per index chunk). Zero leaves
	// the sqlite-vec default. Must be a multiple of 8 when set.
	ChunkSize int
}
