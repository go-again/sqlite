package blobstore

import (
	"errors"
	"sync"

	"github.com/go-again/az"
)

// encoderPool and decoderPool reuse az.Encoders and az.Decoders across chunk
// writes and reads, so a high-throughput stream does not construct a fresh,
// heavyweight lz4/zstd codec per chunk. An az.Encoder/az.Decoder is not safe for
// concurrent use, so a pool hands one to a single goroutine for the duration of
// a single EncodeAll / DecodeAllLimit. Both append into a caller slice and
// return an independent result, so the codec is safe to return to the pool
// immediately.
var (
	encoderPool = sync.Pool{New: func() any { return az.NewEncoder() }}
	decoderPool = sync.Pool{New: func() any { return az.NewDecoder() }}
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

// writeLevel picks the level for compressing a chunk: the object's frozen level
// override when set, otherwise the writing Store's level (defaulting if the
// Store has none). objOverride is the object's stored level column read back as
// a Compression — CompressionNone (0) means "no override".
func writeLevel(objOverride, store Compression) az.Level {
	if lvl, ok := objOverride.azLevel(); ok {
		return lvl
	}
	return store.azLevelOrDefault()
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
	e := encoderPool.Get().(*az.Encoder)
	comp, err := e.EncodeAll(nil, plain, lvl) // EncodeAll output is identical to az.Compress
	encoderPool.Put(e)
	if err != nil {
		return nil, 0, err
	}
	if len(comp) >= len(plain) {
		return plain, encVerbatim, nil
	}
	return comp, encAZ, nil
}

// decodeChunk reverses encodeChunk. enc=encVerbatim returns data unchanged (after
// a length check). enc=encAZ decompresses with a pooled decoder bounded to max: a
// well-formed chunk decompresses to exactly the chunk size, so DecodeAllLimit
// caps the output at max and reports a larger frame (a corrupt or hostile one — a
// decompression bomb) as ErrTooLarge rather than allowing an unbounded allocation.
func decodeChunk(data []byte, enc, max int) ([]byte, error) {
	if enc == encVerbatim {
		if len(data) > max {
			return nil, errors.New("blobstore: verbatim chunk exceeds chunk size (corrupt data)")
		}
		return data, nil
	}
	d := decoderPool.Get().(*az.Decoder)
	plain, err := d.DecodeAllLimit(nil, data, max)
	decoderPool.Put(d)
	if errors.Is(err, az.ErrTooLarge) {
		return nil, errors.New("blobstore: decompressed chunk exceeds chunk size (corrupt data)")
	}
	if err != nil {
		return nil, err
	}
	return plain, nil
}
