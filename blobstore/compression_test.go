package blobstore

import (
	"bytes"
	"context"
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
			`SELECT enc FROM files_chunks WHERE obj=? ORDER BY seq LIMIT 1`, id).Scan(&enc); err != nil {
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
		`SELECT enc, length(data) FROM files_chunks WHERE obj=? AND seq=0`, id).Scan(&enc, &dlen); err != nil {
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
		`SELECT enc FROM files_chunks WHERE obj=? AND seq=0`, compID).Scan(&enc); err != nil {
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
