package vec

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"
)

// encodeJSON serializes a []float32 as the `[v0, v1, ...]` text form
// sqlite-vec parses by default. It uses a fixed 'g' format so we don't lose
// precision on round-trip.
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

// encodeValue routes through encodeJSON or encodeBinary according to enc, and
// returns a value suitable for sql.DB.Exec args. JSON yields a string; Binary
// yields a []byte. Both forms are accepted by sqlite-vec's vec0 module.
func encodeValue(v []float32, enc Encoding) any {
	switch enc {
	case Binary:
		return encodeBinary(v)
	default:
		return encodeJSON(v)
	}
}

// matchPlaceholder returns the SQL fragment used to bind a vector argument on
// the right-hand side of MATCH. JSON needs no wrapping; Binary needs to be
// run through sqlite-vec's vec_f32 constructor so the BLOB is interpreted
// correctly. We bind the actual value as a separate parameter to keep prepared-
// statement caching happy.
func matchPlaceholder(enc Encoding) string {
	if enc == Binary {
		return "vec_f32(?)"
	}
	return "?"
}
