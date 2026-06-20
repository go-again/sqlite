package blobstore

import (
	"bytes"
	"errors"
	"io"

	"github.com/go-again/az"
)

// This file is the ONLY place that touches github.com/go-again/az. The public
// Compression levels are mapped to az here, and chunk payloads are framed and
// unframed here, so an az API change is contained to this file.

// Per-object storage modes, recorded in the objects.codec column.
const (
	codecRaw = 0 // fixed-size zeroblob chunks, in-place incremental BLOB I/O
	codecAZ  = 1 // whole compressed BLOB values per chunk
)

// Per-chunk storage encodings, recorded in the chunks.enc column.
const (
	encVerbatim = 0 // data stored as-is (the incompressible fallback)
	encAZ       = 1 // data is an az-compressed frame
)

// azLevelOrDefault is azLevel with a fallback to the default level — used when
// rewriting a chunk of a compressed object on a Store that has no compression
// configured (reads are level-agnostic, so any level is safe to write).
func (c Compression) azLevelOrDefault() az.Level {
	if lvl, ok := c.azLevel(); ok {
		return lvl
	}
	return az.Level3
}

// azLevel maps a Compression to an az.Level. ok is false for CompressionNone.
func (c Compression) azLevel() (az.Level, bool) {
	switch c {
	case CompressionFastest:
		return az.Level1, true
	case CompressionFast:
		return az.Level2, true
	case CompressionDefault:
		return az.Level3, true
	case CompressionBetter:
		return az.Level4, true
	case CompressionBest:
		return az.Level5, true
	default: // CompressionNone or unknown
		return 0, false
	}
}

// encodeChunk compresses plain at lvl for storage. If compression does not
// shrink it (incompressible or already-compressed data), it returns the
// plaintext verbatim with encVerbatim, so a chunk is never stored larger than
// its payload. The returned slice is bound directly into the INSERT, which
// copies it, so aliasing plain is safe.
func encodeChunk(plain []byte, lvl az.Level) (data []byte, enc int, err error) {
	comp, err := az.Compress(plain, lvl)
	if err != nil {
		return nil, 0, err
	}
	if len(comp) >= len(plain) {
		return plain, encVerbatim, nil
	}
	return comp, encAZ, nil
}

// decodeChunk reverses encodeChunk. enc=encVerbatim returns data unchanged.
// enc=encAZ az-decompresses through a bounded reader: a well-formed chunk
// decompresses to exactly the chunk size, so anything larger than max is a
// corrupt or hostile frame (a decompression bomb) and is rejected rather than
// allowed to allocate without limit.
func decodeChunk(data []byte, enc, max int) ([]byte, error) {
	if enc == encVerbatim {
		return data, nil
	}
	r := az.NewReader(bytes.NewReader(data))
	defer r.Close()
	var buf bytes.Buffer
	buf.Grow(max)
	n, err := io.Copy(&buf, io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if n > int64(max) {
		return nil, errors.New("blobstore: decompressed chunk exceeds chunk size (corrupt data)")
	}
	return buf.Bytes(), nil
}
