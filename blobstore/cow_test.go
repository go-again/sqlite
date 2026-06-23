package blobstore

import (
	"bytes"
	"context"
	"testing"

	sqlite "gosqlite.org"
)

// mustClone clones src and fails the test on error — the public Clone produces
// exactly the shared-block state these copy-on-write tests exercise.
func mustClone(t *testing.T, s *Store, src int64) int64 {
	t.Helper()
	id, err := s.Clone(context.Background(), src)
	if err != nil {
		t.Fatalf("Clone(%d): %v", src, err)
	}
	return id
}

// blockCount is the number of rows in the blocks table — the count of distinct
// stored chunk payloads across the whole store.
func blockCount(t *testing.T, db *sqlite.DB) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM files_blocks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// chunkBlock returns the id and refcount of the block chunk (obj, seq) maps to.
func chunkBlock(t *testing.T, db *sqlite.DB, obj, seq int64) (block, refs int64) {
	t.Helper()
	if err := db.QueryRowContext(context.Background(),
		`SELECT b.id, b.refs FROM files_chunks c JOIN files_blocks b ON c.block = b.id `+
			`WHERE c.obj = ? AND c.seq = ?`, obj, seq).Scan(&block, &refs); err != nil {
		t.Fatalf("chunkBlock(%d, %d): %v", obj, seq, err)
	}
	return block, refs
}

// TestCoWRawIsolation: writing a chunk whose block is shared with a clone copies
// the block first, so the clone's bytes are untouched, refcounts settle to one
// each on the diverged chunk, and an unwritten chunk stays shared.
func TestCoWRawIsolation(t *testing.T) {
	skipUnderRace(t) // exercises modernc's incremental BLOB write (checkptr)
	s, db := newStore(t, WithChunkSize(8))
	ctx := context.Background()
	a, _ := s.Create(ctx)
	writeAt(t, s, a, []byte("AAAAAAAABBBBBBBB"), 0) // two full chunks
	if got := blockCount(t, db); got != 2 {
		t.Fatalf("blocks after write = %d, want 2", got)
	}

	b := mustClone(t, s, a)
	if got := blockCount(t, db); got != 2 {
		t.Fatalf("blocks after clone = %d, want 2 (mappings shared, no copy)", got)
	}
	x0a, r0a := chunkBlock(t, db, a, 0)
	x0b, r0b := chunkBlock(t, db, b, 0)
	if x0a != x0b || r0a != 2 || r0b != 2 {
		t.Fatalf("chunk0 not shared after clone: a=(%d,%d) b=(%d,%d)", x0a, r0a, x0b, r0b)
	}

	writeAt(t, s, b, []byte("ZZZZZZZZ"), 0) // diverge the clone's first chunk
	if got := readAll(t, s, a); string(got) != "AAAAAAAABBBBBBBB" {
		t.Fatalf("clone source mutated by copy-on-write: %q", got)
	}
	if got := readAll(t, s, b); string(got) != "ZZZZZZZZBBBBBBBB" {
		t.Fatalf("clone write landed wrong: %q", got)
	}
	if got := blockCount(t, db); got != 3 {
		t.Fatalf("blocks after copy-on-write = %d, want 3", got)
	}
	n0a, _ := chunkBlock(t, db, a, 0)
	n0b, _ := chunkBlock(t, db, b, 0)
	if n0a == n0b {
		t.Fatalf("chunk0 still shares a block after copy-on-write: %d", n0a)
	}
	if _, ra := chunkBlock(t, db, a, 0); ra != 1 {
		t.Fatalf("source chunk0 refs = %d, want 1", ra)
	}
	if _, rb := chunkBlock(t, db, b, 0); rb != 1 {
		t.Fatalf("clone chunk0 refs = %d, want 1", rb)
	}
	if _, r1 := chunkBlock(t, db, a, 1); r1 != 2 {
		t.Fatalf("untouched chunk1 refs = %d, want 2 (still shared)", r1)
	}
}

// TestCoWPartialRawWrite: a partial write to a shared raw chunk must copy the
// whole block before mutating in place, so the bytes outside the written span
// are preserved (not zeroed) and the source is untouched.
func TestCoWPartialRawWrite(t *testing.T) {
	skipUnderRace(t) // exercises modernc's incremental BLOB write (checkptr)
	s, _ := newStore(t, WithChunkSize(8))
	ctx := context.Background()
	a, _ := s.Create(ctx)
	writeAt(t, s, a, []byte("ABCDEFGH"), 0)
	b := mustClone(t, s, a)

	writeAt(t, s, b, []byte("ZZ"), 2) // overwrite only bytes [2:4]
	if got := readAll(t, s, a); string(got) != "ABCDEFGH" {
		t.Fatalf("source mutated: %q", got)
	}
	if got := readAll(t, s, b); string(got) != "ABZZEFGH" {
		t.Fatalf("partial copy-on-write lost surrounding bytes: %q", got)
	}
}

// TestCoWCompressedIsolation: the compressed write path replaces a shared block
// with a fresh one rather than rewriting it, so a clone keeps its bytes.
func TestCoWCompressedIsolation(t *testing.T) {
	s, db := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault))
	ctx := context.Background()
	a, _ := s.Create(ctx)
	src := bytes.Repeat([]byte("x"), 64)
	writeAt(t, s, a, src, 0)
	b := mustClone(t, s, a)
	if _, r := chunkBlock(t, db, b, 0); r != 2 {
		t.Fatalf("refs after clone = %d, want 2", r)
	}

	repl := bytes.Repeat([]byte("y"), 64)
	writeAt(t, s, b, repl, 0)
	if got := readAll(t, s, a); !bytes.Equal(got, src) {
		t.Fatal("clone source mutated by compressed copy-on-write")
	}
	if got := readAll(t, s, b); !bytes.Equal(got, repl) {
		t.Fatal("clone compressed write landed wrong")
	}
	if _, ra := chunkBlock(t, db, a, 0); ra != 1 {
		t.Fatalf("source refs after copy-on-write = %d, want 1", ra)
	}
}

// TestRefcountFreeOnDelete: deleting one holder of a shared block decrements its
// refcount without freeing it; the block is freed only when the last holder goes.
func TestRefcountFreeOnDelete(t *testing.T) {
	skipUnderRace(t) // exercises modernc's incremental BLOB write (checkptr)
	s, db := newStore(t, WithChunkSize(8))
	ctx := context.Background()
	a, _ := s.Create(ctx)
	writeAt(t, s, a, []byte("AAAAAAAA"), 0)
	b := mustClone(t, s, a)
	if got := blockCount(t, db); got != 1 {
		t.Fatalf("blocks = %d, want 1 (shared)", got)
	}

	if err := s.Delete(ctx, a); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	if got := blockCount(t, db); got != 1 {
		t.Fatalf("blocks after deleting source = %d, want 1 (still held by clone)", got)
	}
	if got := readAll(t, s, b); string(got) != "AAAAAAAA" {
		t.Fatalf("clone data lost after source delete: %q", got)
	}
	if _, r := chunkBlock(t, db, b, 0); r != 1 {
		t.Fatalf("refs after source delete = %d, want 1", r)
	}

	if err := s.Delete(ctx, b); err != nil {
		t.Fatalf("delete clone: %v", err)
	}
	if got := blockCount(t, db); got != 0 {
		t.Fatalf("blocks after deleting last holder = %d, want 0", got)
	}
}

// TestRefcountFreeOnTruncate: a shrinking Truncate releases the dropped chunks'
// blocks by the same refcount rule — a shared block survives until its last
// holder shrinks past it.
func TestRefcountFreeOnTruncate(t *testing.T) {
	skipUnderRace(t) // exercises modernc's incremental BLOB write (checkptr)
	s, db := newStore(t, WithChunkSize(8))
	ctx := context.Background()
	a, _ := s.Create(ctx)
	writeAt(t, s, a, []byte("AAAAAAAABBBBBBBB"), 0) // two chunks
	b := mustClone(t, s, a)
	if got := blockCount(t, db); got != 2 {
		t.Fatalf("blocks = %d, want 2", got)
	}

	if err := s.Truncate(ctx, a, 8); err != nil { // drop the source's chunk1
		t.Fatalf("truncate source: %v", err)
	}
	if got := blockCount(t, db); got != 2 {
		t.Fatalf("blocks after source shrink = %d, want 2 (clone still holds chunk1)", got)
	}
	if got := readAll(t, s, b); string(got) != "AAAAAAAABBBBBBBB" {
		t.Fatalf("clone data lost after source shrink: %q", got)
	}
	if _, r := chunkBlock(t, db, b, 1); r != 1 {
		t.Fatalf("chunk1 refs after source shrink = %d, want 1", r)
	}

	if err := s.Truncate(ctx, b, 8); err != nil { // drop the clone's chunk1 too
		t.Fatalf("truncate clone: %v", err)
	}
	if got := blockCount(t, db); got != 1 {
		t.Fatalf("blocks after last holder shrinks = %d, want 1", got)
	}
}
