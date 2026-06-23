package vault

// codec.go is the only file that imports the compression library, so a change
// to that library's API is contained here.

import (
	"bytes"
	"fmt"
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
// at level c. The az frame checksum is disabled: every stored slot already
// carries a container CRC32C (verified on read) and the decode bounds output to
// the page size, so the frame's own checksum is redundant work and bytes.
func compressStream(dst io.Writer, src io.Reader, c Compression) error {
	w := az.NewWriter(dst, az.WithLevel(c.azLevel()), az.WithChecksum(false))
	if _, err := io.Copy(w, src); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// encodePage encodes a single logical page for storage in a slot. With
// [CompressionNone] (the default) the page is stored raw — compression is off.
// Otherwise it is compressed, and returned verbatim only when it did not shrink,
// so the stored bytes are never larger than the page itself; decode skips the
// codec for a verbatim slot.
func encodePage(page []byte, c Compression) (stored []byte, verbatim bool, err error) {
	if c == CompressionNone {
		return page, true, nil // compression off: store the page raw
	}
	var buf bytes.Buffer
	if err := compressStream(&buf, bytes.NewReader(page), c); err != nil {
		return nil, false, err
	}
	if buf.Len() >= len(page) {
		return page, true, nil
	}
	return buf.Bytes(), false, nil
}

// decodePage decompresses a stored slot into dst, which must be exactly the
// logical page size; decoding is bounded to it (a slot that inflates past the
// page size is corrupt). It is the inverse of [encodePage]'s non-verbatim path.
//
// It decompresses straight into dst's backing array — bytes.NewBuffer(dst[:0])
// writes in place as long as the output stays within len(dst), which the bound
// guarantees for a valid page — so a correct page needs no intermediate buffer
// or copy.
func decodePage(dst, stored []byte) error {
	n, err := decompressStream(bytes.NewBuffer(dst[:0]), bytes.NewReader(stored), int64(len(dst)))
	if err != nil {
		return err
	}
	if int(n) != len(dst) {
		return fmt.Errorf("vault: decoded page is %d bytes, want %d", n, len(dst))
	}
	return nil
}

// decompressStream writes the decompressed contents of the az frame in src into
// dst, auto-detecting LZ4 vs zstd from the frame magic, and returns the number
// of bytes written. If max > 0, decoding errors once the output would exceed
// max bytes — a decompression-bomb guard for untrusted sources. A source too
// short to carry a frame magic (1–3 bytes) is seen by the codec as a cleanly
// empty stream — it returns (0, nil) — so callers that must not treat that as a
// valid empty database check the count.
func decompressStream(dst io.Writer, src io.Reader, max int64) (int64, error) {
	ar := az.NewReader(src)
	var rd io.Reader = ar
	if max > 0 {
		rd = io.LimitReader(ar, max+1)
	}
	n, err := io.Copy(dst, rd)
	if cerr := ar.Close(); err == nil {
		err = cerr
	}
	if err == nil && max > 0 && n > max {
		return n, fmt.Errorf("vault: decompressed output exceeds the %d-byte MaxInflatedSize — refusing (possible decompression bomb)", max)
	}
	return n, err
}
