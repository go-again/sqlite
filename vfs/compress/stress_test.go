package compress

// Increment-4 container-level stress: truncate (shrink + regrow) and sparse
// pages, exercised directly against mainFile over a crashBacking so they are
// fast and deterministic (no SQLite). The allocator-under-VACUUM and ratio
// tests live in vacuum_test.go / ratio_test.go.

import (
	"bytes"
	"testing"
)

// reopenContainer reopens a fresh mainFile over a durable image (the bytes a
// crashBacking had Sync'd), the same way openMainOver recovers on disk.
func reopenContainer(t *testing.T, durable []byte) *mainFile {
	t.Helper()
	f, err := openMainOver(newCrashBacking(durable), false, defaultBlockSize, crashPageSize, CompressionDefault)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	return f
}

// readPage reads one logical page.
func readPage(t *testing.T, f *mainFile, pageNo int) []byte {
	t.Helper()
	buf := make([]byte, crashPageSize)
	if _, err := f.ReadAt(buf, int64(pageNo)*crashPageSize); err != nil {
		t.Fatalf("read page %d: %v", pageNo, err)
	}
	return buf
}

func TestMainFileTruncateShrinkAndGrow(t *testing.T) {
	cb := newCrashBacking(nil)
	f, err := openMainOver(cb, false, defaultBlockSize, crashPageSize, CompressionDefault)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Write pages 0..9 with distinct content.
	for i := range 10 {
		_, _ = f.WriteAt(constPage(byte(0x10+i), crashPageSize), int64(i)*crashPageSize)
	}
	if err := f.Sync(0); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Shrink to 3 pages and commit.
	if err := f.Truncate(3 * crashPageSize); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := f.Sync(0); err != nil {
		t.Fatalf("commit truncate: %v", err)
	}
	if sz, _ := f.Size(); sz != 3*crashPageSize {
		t.Fatalf("size after shrink = %d, want %d", sz, 3*crashPageSize)
	}

	// Reopen: exactly 3 pages, original content intact.
	fr := reopenContainer(t, cb.synced)
	if sz, _ := fr.Size(); sz != 3*crashPageSize {
		t.Fatalf("reopened size = %d, want %d", sz, 3*crashPageSize)
	}
	for i := range 3 {
		if !bytes.Equal(readPage(t, fr, i), constPage(byte(0x10+i), crashPageSize)) {
			t.Fatalf("page %d content changed after shrink", i)
		}
	}

	// Grow back by writing page 7; pages 3..6 become sparse (zero-fill).
	_, _ = fr.WriteAt(constPage(0x77, crashPageSize), 7*crashPageSize)
	if err := fr.Sync(0); err != nil {
		t.Fatalf("commit regrow: %v", err)
	}
	cb2 := bytesOfSync(t, fr)

	fg := reopenContainer(t, cb2)
	if sz, _ := fg.Size(); sz != 8*crashPageSize {
		t.Fatalf("reopened grown size = %d, want %d", sz, 8*crashPageSize)
	}
	zero := make([]byte, crashPageSize)
	for i := 3; i <= 6; i++ {
		if !bytes.Equal(readPage(t, fg, i), zero) {
			t.Fatalf("regrown page %d not zero-filled", i)
		}
	}
	if !bytes.Equal(readPage(t, fg, 7), constPage(0x77, crashPageSize)) {
		t.Fatalf("page 7 content wrong after regrow")
	}
}

func TestMainFileSparsePages(t *testing.T) {
	cb := newCrashBacking(nil)
	f, err := openMainOver(cb, false, defaultBlockSize, crashPageSize, CompressionDefault)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Write page 0 and page 5, leaving 1..4 never written (sparse).
	_, _ = f.WriteAt(constPage(0xAA, crashPageSize), 0)
	_, _ = f.WriteAt(constPage(0xBB, crashPageSize), 5*crashPageSize)
	if err := f.Sync(0); err != nil {
		t.Fatalf("commit: %v", err)
	}

	fr := reopenContainer(t, cb.synced)
	if sz, _ := fr.Size(); sz != 6*crashPageSize {
		t.Fatalf("size = %d, want %d", sz, 6*crashPageSize)
	}
	if !bytes.Equal(readPage(t, fr, 0), constPage(0xAA, crashPageSize)) {
		t.Fatal("page 0 wrong")
	}
	zero := make([]byte, crashPageSize)
	for i := 1; i <= 4; i++ {
		if !bytes.Equal(readPage(t, fr, i), zero) {
			t.Fatalf("sparse page %d not zero-filled", i)
		}
	}
	if !bytes.Equal(readPage(t, fr, 5), constPage(0xBB, crashPageSize)) {
		t.Fatal("page 5 wrong")
	}
}

// bytesOfSync returns the durable image of f's backing (a crashBacking).
func bytesOfSync(t *testing.T, f *mainFile) []byte {
	t.Helper()
	cb, ok := f.back.(*crashBacking)
	if !ok {
		t.Fatalf("backing is not a crashBacking")
	}
	return append([]byte(nil), cb.synced...)
}
