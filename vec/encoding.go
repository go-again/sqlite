package vec

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"
)

// encodeJSON serializes a []float32 as the `[v0,v1,...]` JSON-array text
// form sqlite-vec parses by default. Values are comma-separated with no
// whitespace — the parser accepts either, but skipping spaces keeps the
// payload tighter. 'g' with precision -1 picks the shortest representation
// that uniquely identifies each float32, so round-trips are lossless.
func encodeJSON(v []float32) string {
	var b strings.Builder
	b.Grow(2 + len(v)*16)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		// 'g' with -1 precision picks the shortest representation that
		// uniquely identifies the float32. Critical for round-trip fidelity.
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// encodeBinary serializes a []float32 as a packed little-endian float32 BLOB
// matching what sqlite-vec's vec_f32 SQL constructor expects. Each value is 4
// bytes; the total length is 4*len(v).
func encodeBinary(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf
}

// Encode serializes a []float32 into a value suitable for sql.DB.Exec
// bindings. JSON yields a string; Binary yields a []byte. Both forms are
// accepted by sqlite-vec's vec0 module.
func (e Encoding) Encode(v []float32) any {
	switch e {
	case Binary:
		return encodeBinary(v)
	default:
		return encodeJSON(v)
	}
}

// Placeholder returns the SQL fragment used to bind a vector argument
// on the right-hand side of MATCH. JSON needs no wrapping ("?"); Binary
// needs to be run through sqlite-vec's vec_f32 constructor ("vec_f32(?)")
// so the BLOB is interpreted correctly.
func (e Encoding) Placeholder() string {
	if e == Binary {
		return "vec_f32(?)"
	}
	return "?"
}

// encodeValue / matchPlaceholder are the legacy private helpers — kept
// as thin wrappers so existing call sites compile unchanged. New code
// should call the methods on Encoding directly.
func encodeValue(v []float32, enc Encoding) any { return enc.Encode(v) }
func matchPlaceholder(enc Encoding) string      { return enc.Placeholder() }

// Encode is the package-function form of [Encoding.Placeholder] +
// [Encoding.Encode]. Use it from raw-SQL escape hatches when you're
// composing a query by hand and want the typed encoding pipeline
// without going through [Table.KNN] / [Table.KNNSlice].
//
// Returns the SQL placeholder fragment (`?` for JSON, `vec_f32(?)` for
// Binary) and the bind value that pairs with it (a string for JSON, a
// []byte for Binary). The placeholder is meant for string
// interpolation into your SQL; the value is passed as a parameter to
// `db.Query` / `db.Exec`.
//
// Example:
//
//	ph, val := vec.Encode(embedding, vec.Binary)
//	rows, err := db.QueryContext(ctx,
//	    "SELECT rowid, distance FROM items_vec WHERE embedding MATCH "+ph+
//	        " AND rowid IN (?, ?, ?) ORDER BY distance LIMIT 10",
//	    val, id1, id2, id3)
//
// For the typed path (no manual SQL), use [Table.KNN] / [Table.KNNSlice]
// instead.
func Encode(embedding []float32, enc Encoding) (placeholder string, value any) {
	return enc.Placeholder(), enc.Encode(embedding)
}
