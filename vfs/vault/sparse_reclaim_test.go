package vault

import (
	"bytes"
	"path/filepath"
	"testing"

	sqlite "gosqlite.org"
)

// TestSparseOnWrite: writing an all-zero page stores it sparse (no block), and
// overwriting a written page with zeros frees its block — both read back as zeros and
// survive a reopen.
func TestSparseOnWrite(t *testing.T) {
	const ps = crashPageSize
	cb := newCrashBacking(nil)
	f, err := openMainOver(cb, false, defaultBlockSize, ps, 4, CompressionNone)
	if err != nil {
		t.Fatal(err)
	}
	c := f.c

	// A real page occupies a block; overwriting it with zeros frees the block.
	if _, err := f.WriteAt(constPage(0xAB, ps), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(0); err != nil {
		t.Fatal(err)
	}
	if c.dir[0].physOffset == 0 {
		t.Fatal("a non-zero page should occupy a block")
	}
	freeBefore := c.alloc.freeBlocksTotal()
	if _, err := f.WriteAt(make([]byte, ps), 0); err != nil { // overwrite page 0 with zeros
		t.Fatal(err)
	}
	if err := f.Sync(0); err != nil {
		t.Fatal(err)
	}
	if c.dir[0].physOffset != 0 {
		t.Fatal("overwriting a page with zeros should mark it sparse (no block)")
	}
	if c.alloc.freeBlocksTotal() <= freeBefore {
		t.Fatal("the old block was not returned to the free list")
	}

	// A fresh all-zero page never allocates a block.
	if _, err := f.WriteAt(make([]byte, ps), 5*ps); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(0); err != nil {
		t.Fatal(err)
	}
	if c.dir[5].physOffset != 0 {
		t.Fatal("a never-written all-zero page should not allocate a block")
	}

	// Both read back as zeros, and so does a fresh reopen over the durable image.
	zero := make([]byte, ps)
	got := make([]byte, ps)
	for _, p := range []int64{0, 5} {
		if _, err := f.ReadAt(got, p*ps); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, zero) {
			t.Fatalf("page %d not zero in-memory", p)
		}
	}
	fr, err := openMainOver(newCrashBacking(cb.synced), false, defaultBlockSize, ps, 4, CompressionNone)
	if err != nil {
		t.Fatal(err)
	}
	defer fr.Close()
	for _, p := range []int64{0, 5} {
		if _, err := fr.ReadAt(got, p*ps); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, zero) {
			t.Fatalf("page %d not zero after reopen", p)
		}
	}
}

// TestReclaimableBytes: a churned database reports reclaimable space; a path not open
// in this process errors.
func TestReclaimableBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.db")
	db, err := Open(sqlite.Config{Path: path}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	churn(t, db) // grow then free most of it

	got, err := ReclaimableBytes(path)
	if err != nil {
		t.Fatalf("ReclaimableBytes: %v", err)
	}
	if got <= 0 {
		t.Fatalf("ReclaimableBytes after churn = %d, want > 0", got)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReclaimableBytes(path); err == nil {
		t.Fatal("ReclaimableBytes on a closed database succeeded; want an error")
	}
}
