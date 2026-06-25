package vault

import (
	"path/filepath"
	"testing"
	"time"

	sqlite "gosqlite.org"
)

// TestCheckpoint folds the WAL and reclaims freed tail blocks in one call on an
// encrypted WAL container, leaving the data intact and the database writable.
func TestCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ckpt.db")
	db, err := Open(sqlite.Config{
		Path:    path,
		Pragmas: sqlite.Pragmas{JournalMode: sqlite.JournalWAL, BusyTimeout: 2 * time.Second},
	}, Options{Key: randKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	churn(t, db) // grow, then free most of it

	reclaimed, err := Checkpoint(db, path)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if reclaimed < 0 {
		t.Fatalf("Checkpoint reported negative reclaim %d", reclaimed)
	}
	if n := rowCount(t, db); n != 30 {
		t.Fatalf("row count after Checkpoint = %d, want 30", n)
	}
	mustExec(t, db, `INSERT INTO t(id, blob) VALUES(88888, ?)`, []byte("post-checkpoint"))
	if n := rowCount(t, db); n != 31 {
		t.Fatalf("row count after post-Checkpoint insert = %d, want 31", n)
	}
}

// TestContainerTrim drives the trim mechanism directly over a hand-built allocator,
// so the physical layout is deterministic (the live container's layout depends on
// SQLite's allocation order). It proves the tail-free run is returned, a non-tail
// free run is left alone, maxBytes bounds the release, and the file shrinks.
func TestContainerTrim(t *testing.T) {
	const bs = 512
	mk := func(free []extent, highWater uint64) (*container, *crashBacking) {
		back := newCrashBacking(make([]byte, int(highWater)*bs))
		return &container{back: back, blockSize: bs, alloc: newAllocator(free, highWater)}, back
	}

	// A free run at the tail [15,20) is returned in full; the file shrinks and a
	// second trim is a no-op.
	c, back := mk([]extent{{15, 5}}, 20)
	got, err := c.trim(0)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(5 * bs); got != want {
		t.Fatalf("reclaimed %d, want %d", got, want)
	}
	if sz, _ := back.Size(); sz != 15*bs {
		t.Fatalf("file size %d, want %d", sz, 15*bs)
	}
	if c.alloc.highWater != 15 {
		t.Fatalf("highWater %d, want 15", c.alloc.highWater)
	}
	if again, _ := c.trim(0); again != 0 {
		t.Fatalf("second trim reclaimed %d, want 0", again)
	}

	// maxBytes bounds the release to whole blocks from the tail.
	c, _ = mk([]extent{{10, 10}}, 20)
	if got, _ := c.trim(3 * bs); got != 3*bs {
		t.Fatalf("bounded reclaimed %d, want %d", got, 3*bs)
	}
	if c.alloc.highWater != 17 {
		t.Fatalf("highWater after bounded trim %d, want 17", c.alloc.highWater)
	}

	// A free run that is not at the tail is left untouched.
	if c, _ := mk([]extent{{5, 3}}, 20); func() int64 { n, _ := c.trim(0); return n }() != 0 {
		t.Fatal("a non-tail free run was trimmed")
	}
	// No free runs at all is a no-op.
	if c, _ := mk(nil, 20); func() int64 { n, _ := c.trim(0); return n }() != 0 {
		t.Fatal("trim reclaimed from an allocator with no free runs")
	}

	// With a tail run AND an earlier (non-tail) free run, only the tail run is
	// returned; the earlier one survives, and a second trim is then a no-op.
	c, _ = mk([]extent{{5, 3}, {15, 5}}, 20)
	if got, _ := c.trim(0); got != 5*bs {
		t.Fatalf("multi-extent trim reclaimed %d, want %d", got, 5*bs)
	}
	if c.alloc.highWater != 15 || len(c.alloc.free) != 1 || c.alloc.free[0] != (extent{5, 3}) {
		t.Fatalf("after tail trim: highWater=%d free=%v, want highWater=15 free=[{5 3}]", c.alloc.highWater, c.alloc.free)
	}
	if again, _ := c.trim(0); again != 0 {
		t.Fatalf("trim of a now-non-tail free run reclaimed %d, want 0", again)
	}

	// A read-only container refuses.
	c, _ = mk([]extent{{15, 5}}, 20)
	c.readOnly = true
	if _, err := c.trim(0); err == nil {
		t.Fatal("trim on a read-only container: want an error")
	}
	// A read-only recipient (authenticated, no write authority) also refuses.
	c, _ = mk([]extent{{15, 5}}, 20)
	c.readOnlyRecipient = true
	if _, err := c.trim(0); err == nil {
		t.Fatal("trim on a read-only-recipient container: want an error")
	}
}

// TestTrim exercises the public, registry-based entry point: it reaches the live
// container, never grows the file, leaves the data intact and writable, and errors
// when no database is open at the path. (How much it reclaims depends on SQLite's
// physical layout, which TestContainerTrim covers deterministically.)
func TestTrim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trim.db")
	db, err := Open(sqlite.Config{Path: path}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	churn(t, db) // grow then free most of the file

	before := fileSize(t, path)
	reclaimed, err := Trim(path, 0)
	if err != nil {
		t.Fatalf("Trim: %v", err)
	}
	if reclaimed < 0 {
		t.Fatalf("Trim reported negative reclaim %d", reclaimed)
	}
	after := fileSize(t, path)
	if after > before {
		t.Fatalf("Trim grew the file: before=%d after=%d", before, after)
	}
	if int64(reclaimed) != before-after {
		t.Fatalf("reclaimed %d but file shrank by %d", reclaimed, before-after)
	}
	// Data intact on the open handle, and still writable.
	if n := rowCount(t, db); n != 30 {
		t.Fatalf("row count after Trim = %d, want 30", n)
	}
	mustExec(t, db, `INSERT INTO t(id, blob) VALUES(99999, ?)`, []byte("post-trim"))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Durable: reopens intact with the post-trim row.
	db2, err := Open(sqlite.Config{Path: path}, Options{})
	if err != nil {
		t.Fatalf("reopen after Trim: %v", err)
	}
	defer db2.Close()
	if n := rowCount(t, db2); n != 31 {
		t.Fatalf("row count after reopen = %d, want 31", n)
	}
}

// TestTrimNotOpen: Trim needs the live container; a path not open in this process
// errors (offline Compact is the closed-file reclaim).
func TestTrimNotOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "closed.db")
	db, err := Open(sqlite.Config{Path: path}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `CREATE TABLE t(v)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Trim(path, 0); err == nil {
		t.Fatal("Trim on a closed database succeeded; want an error")
	}
}
