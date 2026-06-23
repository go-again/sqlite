package blobstore

import (
	"bytes"
	"crypto/rand"
	"testing"
)

// TestCompressDecompressExported exercises the exported Compress/Decompress
// helpers: a compressible payload round-trips and shrinks, CompressionNone and
// incompressible input store verbatim (never grown), and Decompress bounds its
// output as a decompression-bomb guard.
func TestCompressDecompressExported(t *testing.T) {
	plain := compressibleBlob(4096)

	data, enc, err := Compress(plain, CompressionDefault)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) >= len(plain) {
		t.Fatalf("compressible payload not shrunk: %d >= %d", len(data), len(plain))
	}
	got, err := Decompress(data, enc, len(plain))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("round-trip mismatch")
	}

	// CompressionNone stores verbatim.
	data, enc, err = Compress(plain, CompressionNone)
	if err != nil {
		t.Fatal(err)
	}
	if enc != encVerbatim || !bytes.Equal(data, plain) {
		t.Fatalf("CompressionNone: enc=%d, verbatim=%v", enc, bytes.Equal(data, plain))
	}

	// Incompressible input is stored verbatim, never larger than the input.
	rnd := make([]byte, 4096)
	if _, err := rand.Read(rnd); err != nil {
		t.Fatal(err)
	}
	data, enc, err = Compress(rnd, CompressionBest)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > len(rnd) {
		t.Fatalf("incompressible payload grown: %d > %d", len(data), len(rnd))
	}
	got, err = Decompress(data, enc, len(rnd))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, rnd) {
		t.Fatal("incompressible round-trip mismatch")
	}

	// Decompress bounds its output: a too-small max is rejected.
	data, enc, err = Compress(compressibleBlob(2048), CompressionDefault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decompress(data, enc, 64); err == nil {
		t.Error("Decompress with a too-small max: want an error")
	}
}
