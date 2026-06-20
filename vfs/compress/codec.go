package compress

// codec.go is the only file that imports the compression library, so a change
// to that library's API is contained here.

import (
	"bytes"
	"io"

	"github.com/go-again/az"
)

// sqliteMagic is the 16-byte prefix every SQLite database file begins with. A
// compressed artifact begins with an az (LZ4/zstd) frame magic instead, so the
// presence of this prefix unambiguously tells a raw database from a compressed
// one.
var sqliteMagic = []byte("SQLite format 3\x00")

// looksLikeSQLite reports whether b begins with the SQLite file magic.
func looksLikeSQLite(b []byte) bool { return bytes.HasPrefix(b, sqliteMagic) }

// azLevel maps the wrapped level to an az.Level. CompressionNone/zero and any
// out-of-range value fall back to the default level.
func (c Compression) azLevel() az.Level {
	switch c {
	case CompressionFastest:
		return az.Level1
	case CompressionFast:
		return az.Level2
	case CompressionBetter:
		return az.Level4
	case CompressionBest:
		return az.Level5
	default: // CompressionNone, CompressionDefault, out of range
		return az.DefaultLevel
	}
}

// compressStream writes everything read from src into dst as a single az frame
// at level c.
func compressStream(dst io.Writer, src io.Reader, c Compression) error {
	w := az.NewWriter(dst, az.WithLevel(c.azLevel()))
	if _, err := io.Copy(w, src); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// decompressStream writes the decompressed contents of the az frame in src into
// dst, auto-detecting LZ4 vs zstd from the frame magic, and returns the number
// of bytes written. A source too short to carry a frame magic (1–3 bytes) is
// seen by the codec as a cleanly empty stream — it returns (0, nil) — so callers
// that must not treat that as a valid empty database check the count.
func decompressStream(dst io.Writer, src io.Reader) (int64, error) {
	r := az.NewReader(src)
	n, err := io.Copy(dst, r)
	if cerr := r.Close(); err == nil {
		err = cerr
	}
	return n, err
}
