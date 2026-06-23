package blobstore

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/go-again/az"
	sqlite "gosqlite.org"
)

// compressibleBlob returns n bytes with long runs that compress well.
func compressibleBlob(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('A' + (i/64)%26)
	}
	return b
}

// incompressibleBlob returns n pseudo-random (xorshift) bytes that don't
// compress — deterministic, no math/rand dependency.
func incompressibleBlob(n int, seed uint32) []byte {
	b := make([]byte, n)
	x := seed
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

func TestCompressRoundTripAllLevels(t *testing.T) {
	data := compressibleBlob(50_000) // spans many 4 KiB chunks
	for _, lvl := range []Compression{CompressionFastest, CompressionFast, CompressionDefault, CompressionBetter, CompressionBest} {
		s, db := newStore(t, WithChunkSize(4096), WithCompression(lvl))
		ctx := context.Background()
		id, _ := s.Create(ctx)
		writeAt(t, s, id, data, 0)
		if got := readAll(t, s, id); !bytes.Equal(got, data) {
			t.Fatalf("level %d: round-trip mismatch (len got=%d want=%d)", lvl, len(got), len(data))
		}
		var enc int
		if err := db.QueryRowContext(ctx,
			`SELECT b.enc FROM files_chunks c JOIN files_blocks b ON c.block=b.id WHERE c.obj=? ORDER BY c.seq LIMIT 1`, id).Scan(&enc); err != nil {
			t.Fatal(err)
		}
		if enc != encAZ {
			t.Fatalf("level %d: compressible data should be stored compressed, enc=%d", lvl, enc)
		}
	}
}

func TestCompressMultiChunkAndSlice(t *testing.T) {
	s, _ := newStore(t, WithChunkSize(8), WithCompression(CompressionDefault))
	ctx := context.Background()
	id, _ := s.Create(ctx)
	want := []byte("ABCDEFGHIJKLMNOPQRST") // 20 bytes over 8-byte chunks
	writeAt(t, s, id, want, 0)
	if got := readAll(t, s, id); !bytes.Equal(got, want) {
		t.Fatalf("full read = %q", got)
	}
	r, _ := s.Reader(ctx, id)
	defer r.Close()
	buf := make([]byte, 6)
	if n, err := r.ReadAt(buf, 5); err != nil || n != 6 {
		t.Fatalf("ReadAt(5,6) = (%d,%v)", n, err)
	}
	if !bytes.Equal(buf, want[5:11]) {
		t.Fatalf("slice = %q, want %q", buf, want[5:11])
	}
}

func TestCompressSparseHoles(t *testing.T) {
	s, _ := newStore(t, WithChunkSize(8), WithCompression(CompressionDefault))
	ctx := context.Background()
	id, _ := s.Create(ctx)
	writeAt(t, s, id, []byte("BBBB"), 16)
	writeAt(t, s, id, []byte("AAAA"), 0)
	got := readAll(t, s, id)
	want := make([]byte, 20)
	copy(want, "AAAA")
	copy(want[16:], "BBBB")
	if !bytes.Equal(got, want) {
		t.Fatalf("sparse compressed read = %v, want %v", got, want)
	}
}

func TestCompressFullChunkFastPathAndPartialRMW(t *testing.T) {
	s, _ := newStore(t, WithChunkSize(8), WithCompression(CompressionDefault))
	ctx := context.Background()
	id, _ := s.Create(ctx)
	writeAt(t, s, id, []byte("01234567"), 0) // full chunk 0 — fast path, no read
	writeAt(t, s, id, []byte("XY"), 2)       // partial into chunk 0 — RMW
	if got := readAll(t, s, id); string(got) != "01XY4567" {
		t.Fatalf("got %q, want 01XY4567", got)
	}
}

func TestCompressIncompressibleFallback(t *testing.T) {
	s, db := newStore(t, WithChunkSize(4096), WithCompression(CompressionBest))
	ctx := context.Background()
	id, _ := s.Create(ctx)
	data := incompressibleBlob(4096, 2463534242)
	writeAt(t, s, id, data, 0)
	if got := readAll(t, s, id); !bytes.Equal(got, data) {
		t.Fatal("incompressible round-trip mismatch")
	}
	var enc, dlen int
	if err := db.QueryRowContext(ctx,
		`SELECT b.enc, length(b.data) FROM files_chunks c JOIN files_blocks b ON c.block=b.id WHERE c.obj=? AND c.seq=0`, id).Scan(&enc, &dlen); err != nil {
		t.Fatal(err)
	}
	if enc != encVerbatim {
		t.Fatalf("incompressible chunk should be stored verbatim, enc=%d", enc)
	}
	if dlen > 4096 {
		t.Fatalf("stored %d bytes for a 4096-byte chunk — expansion", dlen)
	}
}

func TestCompressTruncateShrinkGrow(t *testing.T) {
	s, _ := newStore(t, WithChunkSize(8), WithCompression(CompressionDefault))
	ctx := context.Background()
	id, _ := s.Create(ctx)
	writeAt(t, s, id, []byte("0123456789ABCDEFGHIJKLMNOPQRST"), 0) // 30
	if err := s.Truncate(ctx, id, 10); err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if got := readAll(t, s, id); string(got) != "0123456789" {
		t.Fatalf("after shrink = %q", got)
	}
	if err := s.Truncate(ctx, id, 30); err != nil {
		t.Fatalf("grow: %v", err)
	}
	got := readAll(t, s, id)
	if string(got[:10]) != "0123456789" {
		t.Fatalf("kept prefix = %q", got[:10])
	}
	for i := 10; i < 30; i++ {
		if got[i] != 0 {
			t.Fatalf("byte %d = %d after regrow, want 0", i, got[i])
		}
	}
}

func TestCompressReattachAndMixedModes(t *testing.T) {
	skipUnderRace(t) // exercises a raw object too (OpenBlob trips checkptr under -race)
	db, err := sqlite.OpenWAL(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	payload := compressibleBlob(5000)

	rawStore, err := Open(db, "files") // raw objects
	if err != nil {
		t.Fatal(err)
	}
	compStore, err := Open(db, "files", WithCompression(CompressionBest)) // compressed objects
	if err != nil {
		t.Fatal(err)
	}

	rawID, _ := rawStore.Create(ctx)
	compID, _ := compStore.Create(ctx)
	writeAt(t, rawStore, rawID, payload, 0)
	writeAt(t, compStore, compID, payload, 0)

	// Either store reads either object — the mode is per-object (codec column).
	if !bytes.Equal(readAll(t, compStore, rawID), payload) {
		t.Fatal("compressed store failed to read a raw object")
	}
	if !bytes.Equal(readAll(t, rawStore, compID), payload) {
		t.Fatal("raw store failed to read a compressed object")
	}

	// Writing to the compressed object through the no-compression store
	// recompresses (object codec is authoritative, not the Store setting).
	writeAt(t, rawStore, compID, []byte("zzzz"), 0)
	if got := readAll(t, rawStore, compID); !bytes.Equal(got[:4], []byte("zzzz")) {
		t.Fatalf("rewrite via raw store: prefix = %q", got[:4])
	}
	var enc int
	if err := db.QueryRowContext(ctx,
		`SELECT b.enc FROM files_chunks c JOIN files_blocks b ON c.block=b.id WHERE c.obj=? AND c.seq=0`, compID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if enc != encAZ {
		t.Fatalf("compressed object's chunk should stay compressed after rewrite, enc=%d", enc)
	}
}

func TestDecodeChunkBoundRejectsBomb(t *testing.T) {
	data, enc, err := encodeChunk(make([]byte, 200), az.Level3) // 200 zeros → compresses
	if err != nil {
		t.Fatal(err)
	}
	if enc != encAZ {
		t.Skip("zeros unexpectedly stored verbatim")
	}
	if _, err := decodeChunk(data, enc, 64); err == nil {
		t.Fatal("decodeChunk: want error for a frame that exceeds max (bomb defense)")
	}
	if out, err := decodeChunk(data, enc, 200); err != nil || len(out) != 200 {
		t.Fatalf("decodeChunk within bound = (len %d, %v)", len(out), err)
	}
}

// objectCodec returns the stored per-object codec (codecRaw / codecAZ).
func objectCodec(t *testing.T, db *sqlite.DB, id int64) int {
	t.Helper()
	var codec int
	if err := db.QueryRow(`SELECT codec FROM files_objects WHERE id=?`, id).Scan(&codec); err != nil {
		t.Fatal(err)
	}
	return codec
}

// chunkEnc returns the per-chunk encoding of an object's first chunk.
func chunkEnc(t *testing.T, db *sqlite.DB, id int64) int {
	t.Helper()
	var enc int
	if err := db.QueryRow(`SELECT b.enc FROM files_chunks c JOIN files_blocks b ON c.block=b.id WHERE c.obj=? AND c.seq=0`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	return enc
}

// TestObjectCompressionOverride: a compressed-default Store can create an
// individual raw object via WithObjectCompression(CompressionNone) — the
// "leave hot files raw, compress the rest" case — and both round-trip.
func TestObjectCompressionOverride(t *testing.T) {
	skipUnderRace(t) // the raw object uses incremental BLOB I/O (checkptr trips under -race)
	s, db := newStore(t, WithChunkSize(4096), WithCompression(CompressionBest))
	ctx := context.Background()
	data := compressibleBlob(20_000)

	comp, _ := s.Create(ctx)                                        // Store default → compressed
	raw, _ := s.Create(ctx, WithObjectCompression(CompressionNone)) // override → raw

	writeAt(t, s, comp, data, 0)
	writeAt(t, s, raw, data, 0)

	if !bytes.Equal(readAll(t, s, comp), data) {
		t.Fatal("compressed object round-trip mismatch")
	}
	if !bytes.Equal(readAll(t, s, raw), data) {
		t.Fatal("raw override object round-trip mismatch")
	}
	if c := objectCodec(t, db, comp); c != codecAZ {
		t.Fatalf("default object codec = %d, want az (%d)", c, codecAZ)
	}
	if c := objectCodec(t, db, raw); c != codecRaw {
		t.Fatalf("WithObjectCompression(None) object codec = %d, want raw (%d)", c, codecRaw)
	}
	if enc := chunkEnc(t, db, comp); enc != encAZ {
		t.Fatalf("compressed object's first chunk enc = %d, want az (%d)", enc, encAZ)
	}
}

// TestObjectCompressionForceCompressedOnRawStore: a raw-default Store can create
// an individual compressed object via WithObjectCompression(level).
func TestObjectCompressionForceCompressedOnRawStore(t *testing.T) {
	s, db := newStore(t, WithChunkSize(4096)) // raw default
	ctx := context.Background()
	data := compressibleBlob(20_000)

	id, _ := s.Create(ctx, WithObjectCompression(CompressionDefault)) // override → compressed
	writeAt(t, s, id, data, 0)

	if !bytes.Equal(readAll(t, s, id), data) {
		t.Fatal("forced-compressed object round-trip mismatch")
	}
	if c := objectCodec(t, db, id); c != codecAZ {
		t.Fatalf("forced-compressed object codec = %d, want az (%d)", c, codecAZ)
	}
	if enc := chunkEnc(t, db, id); enc != encAZ {
		t.Fatalf("forced-compressed object's first chunk enc = %d, want az (%d)", enc, encAZ)
	}
}

// objectLevel returns the stored per-object compression-level override.
func objectLevel(t *testing.T, db *sqlite.DB, id int64) int {
	t.Helper()
	var level int
	if err := db.QueryRow(`SELECT level FROM files_objects WHERE id=?`, id).Scan(&level); err != nil {
		t.Fatal(err)
	}
	return level
}

// chunkData returns the raw stored bytes of an object's first chunk.
func chunkData(t *testing.T, db *sqlite.DB, id int64) []byte {
	t.Helper()
	var data []byte
	if err := db.QueryRow(`SELECT b.data FROM files_chunks c JOIN files_blocks b ON c.block=b.id WHERE c.obj=? AND c.seq=0`, id).Scan(&data); err != nil {
		t.Fatal(err)
	}
	return data
}

// TestObjectCompressionPerLevel: two objects in one store, created at different
// levels, are each compressed at their own frozen level — not the Store's.
func TestObjectCompressionPerLevel(t *testing.T) {
	s, db := newStore(t, WithChunkSize(64<<10)) // raw-default store; payload fits one chunk
	ctx := context.Background()
	data := compressibleBlob(40_000)

	best, _ := s.Create(ctx, WithObjectCompression(CompressionBest))
	fast, _ := s.Create(ctx, WithObjectCompression(CompressionFastest))
	writeAt(t, s, best, data, 0)
	writeAt(t, s, fast, data, 0)

	if !bytes.Equal(readAll(t, s, best), data) || !bytes.Equal(readAll(t, s, fast), data) {
		t.Fatal("per-level round-trip mismatch")
	}
	if l := objectLevel(t, db, best); l != int(CompressionBest) {
		t.Fatalf("Best object level column = %d, want %d", l, int(CompressionBest))
	}
	if l := objectLevel(t, db, fast); l != int(CompressionFastest) {
		t.Fatalf("Fastest object level column = %d, want %d", l, int(CompressionFastest))
	}
	// Best (zstd) and Fastest (lz4) produce different stored bytes for the same
	// input — proof each object was compressed at its OWN level/codec, not a
	// shared store level. (Which is smaller is data-dependent, so don't assert
	// that — only that they differ.)
	if bytes.Equal(chunkData(t, db, best), chunkData(t, db, fast)) {
		t.Fatal("Best and Fastest objects stored identical bytes — per-object level not applied")
	}
}

// TestSetCompressionChangesLevel: a head written at Best, then the level lowered
// to Default and a tail appended — both round-trip (reads are level-agnostic),
// and the object's stored level reflects the change.
func TestSetCompressionChangesLevel(t *testing.T) {
	const chunk = 64 << 10
	s, db := newStore(t, WithChunkSize(chunk)) // raw-default store
	ctx := context.Background()
	data := compressibleBlob(20_000)

	id, _ := s.Create(ctx, WithObjectCompression(CompressionBest))
	writeAt(t, s, id, data, 0) // chunk 0 @ Best
	if l := objectLevel(t, db, id); l != int(CompressionBest) {
		t.Fatalf("initial level = %d, want %d", l, int(CompressionBest))
	}

	if err := s.SetCompression(ctx, id, CompressionDefault); err != nil {
		t.Fatalf("SetCompression: %v", err)
	}
	if l := objectLevel(t, db, id); l != int(CompressionDefault) {
		t.Fatalf("level after change = %d, want %d", l, int(CompressionDefault))
	}
	writeAt(t, s, id, data, chunk) // chunk 1 @ Default

	want := make([]byte, chunk+len(data))
	copy(want, data)
	copy(want[chunk:], data)
	if got := readAll(t, s, id); !bytes.Equal(got, want) {
		t.Fatal("mixed-level object did not round-trip")
	}

	// A missing object still reports ErrNotFound.
	if err := s.SetCompression(ctx, 999, CompressionDefault); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetCompression on a missing id = %v, want ErrNotFound", err)
	}
}

// TestSetCompressionConvertsMode: SetCompression now changes the storage MODE,
// not just the level. A raw object converts to compressed and back, every
// existing chunk rewritten, content preserved, and later writes honor the new
// mode.
func TestSetCompressionConvertsMode(t *testing.T) {
	skipUnderRace(t) // writes a raw object (OpenBlob trips checkptr under -race)
	const chunk = 4096
	s, db := newStore(t, WithChunkSize(chunk)) // raw-default store
	ctx := context.Background()
	data := compressibleBlob(3*chunk + 100) // several chunks + a partial tail

	id, _ := s.Create(ctx) // raw
	writeAt(t, s, id, data, 0)
	if c := objectCodec(t, db, id); c != codecRaw {
		t.Fatalf("created object codec = %d, want raw", c)
	}
	rawStat, _ := s.Stat(ctx, id)

	// raw -> compressed: every chunk rewritten, content preserved, now smaller.
	if err := s.SetCompression(ctx, id, CompressionBest); err != nil {
		t.Fatalf("SetCompression to Best: %v", err)
	}
	if c := objectCodec(t, db, id); c != codecAZ {
		t.Fatalf("after convert codec = %d, want az", c)
	}
	if !bytes.Equal(readAll(t, s, id), data) {
		t.Fatal("raw->compressed lost data")
	}
	comp, _ := s.Stat(ctx, id)
	if !comp.Compressed || comp.Level != CompressionBest {
		t.Fatalf("after convert Stat = %+v", comp)
	}
	if comp.StoredBytes >= rawStat.StoredBytes {
		t.Fatalf("compressed (%d) not smaller than raw (%d)", comp.StoredBytes, rawStat.StoredBytes)
	}

	// A further write lands compressed and reads back with the rest.
	more := compressibleBlob(chunk)
	writeAt(t, s, id, more, int64(len(data)))
	want := append(append([]byte{}, data...), more...)
	if !bytes.Equal(readAll(t, s, id), want) {
		t.Fatal("post-convert write did not round-trip")
	}

	// compressed -> raw: convert back, content preserved, mode raw again.
	if err := s.SetCompression(ctx, id, CompressionNone); err != nil {
		t.Fatalf("SetCompression to None: %v", err)
	}
	if c := objectCodec(t, db, id); c != codecRaw {
		t.Fatalf("after convert-to-raw codec = %d, want raw", c)
	}
	back, _ := s.Stat(ctx, id)
	if back.Compressed || back.Level != CompressionNone {
		t.Fatalf("after convert-to-raw Stat = %+v", back)
	}
	if !bytes.Equal(readAll(t, s, id), want) {
		t.Fatal("compressed->raw lost data")
	}
}

// TestSetCompressionConvertIncompressible: converting a raw object whose data
// does not compress into compressed mode falls back to verbatim per chunk (no
// expansion) and still round-trips.
func TestSetCompressionConvertIncompressible(t *testing.T) {
	skipUnderRace(t) // writes a raw object (OpenBlob trips checkptr under -race)
	const chunk = 4096
	s, db := newStore(t, WithChunkSize(chunk))
	ctx := context.Background()
	data := incompressibleBlob(2*chunk, 7)

	id, _ := s.Create(ctx)
	writeAt(t, s, id, data, 0)
	if err := s.SetCompression(ctx, id, CompressionBest); err != nil {
		t.Fatalf("SetCompression: %v", err)
	}
	if enc := chunkEnc(t, db, id); enc != encVerbatim {
		t.Fatalf("incompressible chunk enc = %d, want verbatim", enc)
	}
	if !bytes.Equal(readAll(t, s, id), data) {
		t.Fatal("incompressible convert lost data")
	}
}

// TestStatRatioAndMetadata: Stat reports the actual at-rest ratio (computed from
// chunk sizes) and the object's metadata.
func TestStatRatioAndMetadata(t *testing.T) {
	skipUnderRace(t) // writes a raw object (OpenBlob trips checkptr under -race)
	s, _ := newStore(t, WithChunkSize(4096))
	ctx := context.Background()
	data := compressibleBlob(20_000)

	comp, _ := s.Create(ctx, WithObjectCompression(CompressionBest))
	writeAt(t, s, comp, data, 0)
	ci, err := s.Stat(ctx, comp)
	if err != nil {
		t.Fatalf("Stat compressed: %v", err)
	}
	if ci.Size != int64(len(data)) || ci.ChunkSize != 4096 || !ci.Compressed || ci.Level != CompressionBest {
		t.Fatalf("compressed Stat = %+v", ci)
	}
	if ci.StoredBytes >= ci.Size || ci.Ratio <= 0 || ci.Ratio >= 1 {
		t.Fatalf("compressed object not smaller at rest: stored=%d size=%d ratio=%.3f", ci.StoredBytes, ci.Size, ci.Ratio)
	}

	raw, _ := s.Create(ctx) // raw
	writeAt(t, s, raw, data, 0)
	ri, err := s.Stat(ctx, raw)
	if err != nil {
		t.Fatalf("Stat raw: %v", err)
	}
	if ri.Compressed {
		t.Fatalf("raw object reported as compressed: %+v", ri)
	}
	if ri.Ratio <= ci.Ratio || ri.StoredBytes <= ci.StoredBytes {
		t.Fatalf("raw (ratio %.3f, stored %d) should exceed compressed (ratio %.3f, stored %d)",
			ri.Ratio, ri.StoredBytes, ci.Ratio, ci.StoredBytes)
	}
}

func TestEncodeChunkVerbatimFallback(t *testing.T) {
	data := incompressibleBlob(4096, 99)
	out, enc, err := encodeChunk(data, az.Level5)
	if err != nil {
		t.Fatal(err)
	}
	if enc != encVerbatim {
		t.Fatalf("incompressible data should fall back to verbatim, enc=%d", enc)
	}
	if len(out) > len(data) {
		t.Fatalf("verbatim output (%d) larger than input (%d)", len(out), len(data))
	}
}
