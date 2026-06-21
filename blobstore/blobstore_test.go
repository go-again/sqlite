package blobstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"

	sqlite "gosqlite.org"
	"gosqlite.org/internal/raceskip"
)

// skipUnderRace skips tests that exercise BLOB I/O: under -race Go's checkptr
// analyzer rejects the pointer arithmetic in modernc's transpiled
// sqlite3_blob_write/read (an upstream limitation, same as the root package's
// OpenBlob tests). Metadata-only tests still run under -race.
func skipUnderRace(t *testing.T) {
	if raceskip.Enabled {
		t.Skip("skipping under -race: modernc BLOB I/O path trips Go's checkptr analyzer (upstream)")
	}
}

// newStore opens a file-backed WAL database (so every pooled connection
// shares the same store) and returns a Store plus the live DB.
func newStore(t *testing.T, opts ...Option) (*Store, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.OpenWAL(filepath.Join(t.TempDir(), "blob.db"))
	if err != nil {
		t.Fatalf("OpenWAL: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(db, "files", opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, db
}

// readAll returns the full logical content of object id.
func readAll(t *testing.T, s *Store, id int64) []byte {
	t.Helper()
	ctx := context.Background()
	size, err := s.Size(ctx, id)
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	r, err := s.Reader(ctx, id)
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(io.NewSectionReader(r, 0, size))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return got
}

func writeAt(t *testing.T, s *Store, id int64, p []byte, off int64) {
	t.Helper()
	w, err := s.Writer(context.Background(), id)
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	defer w.Close()
	if n, err := w.WriteAt(p, off); err != nil || n != len(p) {
		t.Fatalf("WriteAt(off=%d) = (%d, %v), want (%d, nil)", off, n, err, len(p))
	}
}

func TestRoundTrip(t *testing.T) {
	skipUnderRace(t)
	s, _ := newStore(t)
	ctx := context.Background()
	id, err := s.Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := []byte("hello, blobstore world")
	writeAt(t, s, id, want, 0)

	if size, _ := s.Size(ctx, id); size != int64(len(want)) {
		t.Fatalf("Size = %d, want %d", size, len(want))
	}
	if got := readAll(t, s, id); !bytes.Equal(got, want) {
		t.Fatalf("readAll = %q, want %q", got, want)
	}
}

func TestMultiChunkSpan(t *testing.T) {
	skipUnderRace(t)
	s, _ := newStore(t, WithChunkSize(8))
	ctx := context.Background()
	id, _ := s.Create(ctx)
	// 20 bytes spans chunks 0,1,2 with an 8-byte chunk.
	want := []byte("ABCDEFGHIJKLMNOPQRST")
	writeAt(t, s, id, want, 0)

	if got := readAll(t, s, id); !bytes.Equal(got, want) {
		t.Fatalf("full read = %q, want %q", got, want)
	}
	// Slice read across a chunk boundary.
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

func TestOutOfOrderAndSparseHoles(t *testing.T) {
	skipUnderRace(t)
	s, _ := newStore(t, WithChunkSize(8))
	ctx := context.Background()
	id, _ := s.Create(ctx)

	// Write the tail first, then the head — out of order, leaving a hole.
	writeAt(t, s, id, []byte("BBBB"), 16) // bytes 16..19
	writeAt(t, s, id, []byte("AAAA"), 0)  // bytes 0..3

	if size, _ := s.Size(ctx, id); size != 20 {
		t.Fatalf("Size = %d, want 20", size)
	}
	got := readAll(t, s, id)
	want := make([]byte, 20)
	copy(want[0:], "AAAA")
	copy(want[16:], "BBBB")
	// bytes 4..15 are an untouched hole and must read as zero.
	if !bytes.Equal(got, want) {
		t.Fatalf("sparse read = %v, want %v", got, want)
	}
}

func TestGrowPastEnd(t *testing.T) {
	skipUnderRace(t)
	s, _ := newStore(t, WithChunkSize(8))
	ctx := context.Background()
	id, _ := s.Create(ctx)

	writeAt(t, s, id, []byte("END!"), 100) // sparse grow to size 104
	if size, _ := s.Size(ctx, id); size != 104 {
		t.Fatalf("Size = %d, want 104", size)
	}
	got := readAll(t, s, id)
	if len(got) != 104 {
		t.Fatalf("len = %d, want 104", len(got))
	}
	if !bytes.Equal(got[100:], []byte("END!")) {
		t.Fatalf("tail = %q", got[100:])
	}
	for i, b := range got[:100] {
		if b != 0 {
			t.Fatalf("byte %d = %d, want 0 (sparse)", i, b)
		}
	}
}

func TestTruncateShrinkThenRegrow(t *testing.T) {
	skipUnderRace(t)
	s, _ := newStore(t, WithChunkSize(8))
	ctx := context.Background()
	id, _ := s.Create(ctx)

	full := []byte("0123456789ABCDEFGHIJKLMNOPQRST") // 30 bytes
	writeAt(t, s, id, full, 0)

	if err := s.Truncate(ctx, id, 10); err != nil {
		t.Fatalf("Truncate shrink: %v", err)
	}
	if size, _ := s.Size(ctx, id); size != 10 {
		t.Fatalf("Size after shrink = %d, want 10", size)
	}
	if got := readAll(t, s, id); !bytes.Equal(got, full[:10]) {
		t.Fatalf("after shrink = %q, want %q", got, full[:10])
	}

	// Re-grow: the bytes between the old shrink point and the new end must
	// read as zero (deleted chunks gone, boundary-chunk tail zeroed).
	if err := s.Truncate(ctx, id, 30); err != nil {
		t.Fatalf("Truncate grow: %v", err)
	}
	got := readAll(t, s, id)
	if !bytes.Equal(got[:10], full[:10]) {
		t.Fatalf("kept prefix = %q, want %q", got[:10], full[:10])
	}
	for i := 10; i < 30; i++ {
		if got[i] != 0 {
			t.Fatalf("byte %d = %d after regrow, want 0", i, got[i])
		}
	}
}

func TestReadAtEOFSemantics(t *testing.T) {
	skipUnderRace(t)
	s, _ := newStore(t)
	ctx := context.Background()
	id, _ := s.Create(ctx)
	writeAt(t, s, id, []byte("abcd"), 0)

	r, _ := s.Reader(ctx, id)
	defer r.Close()

	// Read exactly to the end: full data plus io.EOF is permitted by the
	// io.ReaderAt contract.
	buf := make([]byte, 4)
	n, err := r.ReadAt(buf, 0)
	if n != 4 || (err != nil && !errors.Is(err, io.EOF)) {
		t.Fatalf("ReadAt to end = (%d, %v)", n, err)
	}
	// Read entirely past the end.
	if n, err := r.ReadAt(buf, 4); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt past end = (%d, %v), want (0, EOF)", n, err)
	}
}

func TestDelete(t *testing.T) {
	skipUnderRace(t)
	s, _ := newStore(t)
	ctx := context.Background()
	id, _ := s.Create(ctx)
	writeAt(t, s, id, bytes.Repeat([]byte("x"), 200), 0)

	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Size(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Size after delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestNotFound(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	const bad = int64(99999)
	if _, err := s.Size(ctx, bad); !errors.Is(err, ErrNotFound) {
		t.Errorf("Size: %v", err)
	}
	if _, err := s.Reader(ctx, bad); !errors.Is(err, ErrNotFound) {
		t.Errorf("Reader: %v", err)
	}
	if _, err := s.Writer(ctx, bad); !errors.Is(err, ErrNotFound) {
		t.Errorf("Writer: %v", err)
	}
	if err := s.Truncate(ctx, bad, 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("Truncate: %v", err)
	}
}

func TestOpenInvalidName(t *testing.T) {
	db, err := sqlite.OpenWAL(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, bad := range []string{"", "1abc", "has space", "a-b", `a"b`, "a;b"} {
		if _, err := Open(db, bad); err == nil {
			t.Errorf("Open(%q): want error, got nil", bad)
		}
	}
	for _, ok := range []string{"files", "_f", "F1", "blob_store_2"} {
		if _, err := Open(db, ok); err != nil {
			t.Errorf("Open(%q): unexpected error %v", ok, err)
		}
	}
	if _, err := Open(db, "files", WithChunkSize(0)); err == nil {
		t.Error("WithChunkSize(0): want error")
	}
}

func TestReattachAcrossStore(t *testing.T) {
	skipUnderRace(t)
	db, err := sqlite.OpenWAL(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	s1, err := Open(db, "files")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s1.Create(ctx)
	writeAt(t, s1, id, []byte("persisted"), 0)

	// A second Store over the same db + name reattaches to existing objects.
	s2, err := Open(db, "files")
	if err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s2, id); !bytes.Equal(got, []byte("persisted")) {
		t.Fatalf("reattach read = %q", got)
	}
}

func TestConcurrentDistinctObjects(t *testing.T) {
	skipUnderRace(t)
	s, _ := newStore(t, WithChunkSize(16))
	ctx := context.Background()

	const n = 16
	ids := make([]int64, n)
	payloads := make([][]byte, n)
	for i := range ids {
		id, err := s.Create(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
		payloads[i] = bytes.Repeat([]byte{byte('A' + i)}, 100+i)
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w, err := s.Writer(ctx, ids[i])
			if err != nil {
				errs[i] = err
				return
			}
			defer w.Close()
			// Write in two out-of-order halves to exercise growth.
			half := len(payloads[i]) / 2
			if _, err := w.WriteAt(payloads[i][half:], int64(half)); err != nil {
				errs[i] = err
				return
			}
			if _, err := w.WriteAt(payloads[i][:half], 0); err != nil {
				errs[i] = err
			}
		}(i)
	}
	wg.Wait()

	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("object %d: %v", i, errs[i])
		}
		if got := readAll(t, s, ids[i]); !bytes.Equal(got, payloads[i]) {
			t.Fatalf("object %d mismatch (len got=%d want=%d)", i, len(got), len(payloads[i]))
		}
	}
}

// TestConcurrentCompressedRoundTrip exercises the pooled az.Encoder and
// az.Decoder under concurrency: many goroutines each compress and write a
// distinct object, then many goroutines each read one back. Compressed objects
// use whole-value chunk I/O (no OpenBlob), so this runs under -race and validates
// that both codec pools are used safely (one codec per goroutine at a time).
func TestConcurrentCompressedRoundTrip(t *testing.T) {
	s, _ := newStore(t, WithCompression(CompressionBest), WithChunkSize(4096))
	ctx := context.Background()

	const n = 16
	ids := make([]int64, n)
	payloads := make([][]byte, n)
	for i := range ids {
		id, err := s.Create(ctx) // compressed (Store default)
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
		payloads[i] = compressibleBlob(10_000 + i*97) // spans several chunks
	}

	// Concurrent writes exercise the pooled encoder.
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w, err := s.Writer(ctx, ids[i])
			if err != nil {
				errs[i] = err
				return
			}
			defer w.Close()
			_, errs[i] = w.WriteAt(payloads[i], 0)
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("write object %d: %v", i, errs[i])
		}
	}

	// Concurrent reads exercise the pooled decoder.
	got := make([][]byte, n)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := s.Reader(ctx, ids[i])
			if err != nil {
				errs[i] = err
				return
			}
			defer r.Close()
			buf := make([]byte, len(payloads[i]))
			if _, err := r.ReadAt(buf, 0); err != nil && err != io.EOF {
				errs[i] = err
				return
			}
			got[i] = buf
		}(i)
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("read object %d: %v", i, errs[i])
		}
		if !bytes.Equal(got[i], payloads[i]) {
			t.Fatalf("object %d round-trip mismatch", i)
		}
	}
}

// TestBatchRoundTrip: several WriteAt calls inside one Batch commit together and
// read back, including out-of-order and chunk-spanning writes.
func TestBatchRoundTrip(t *testing.T) {
	skipUnderRace(t) // raw object uses OpenBlob
	const chunk = 16
	s, _ := newStore(t, WithChunkSize(chunk))
	ctx := context.Background()
	id, _ := s.Create(ctx)

	want := compressibleBlob(5*chunk + 7) // several chunks + a partial tail
	if err := s.Batch(ctx, id, func(w io.WriterAt) error {
		if _, err := w.WriteAt(want[chunk:], chunk); err != nil { // tail first
			return err
		}
		_, err := w.WriteAt(want[:chunk], 0) // then head
		return err
	}); err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if got := readAll(t, s, id); !bytes.Equal(got, want) {
		t.Fatalf("Batch round-trip mismatch (got %d, want %d)", len(got), len(want))
	}
	if sz, _ := s.Size(ctx, id); sz != int64(len(want)) {
		t.Fatalf("size after batch = %d, want %d", sz, len(want))
	}
}

// TestBatchAtomicRollback: a Batch that fails after writing leaves the object
// exactly as it was before the batch (all-or-nothing). Uses a compressed object
// so it runs under -race.
func TestBatchAtomicRollback(t *testing.T) {
	const chunk = 64
	s, _ := newStore(t, WithCompression(CompressionBest), WithChunkSize(chunk))
	ctx := context.Background()
	id, _ := s.Create(ctx)

	orig := compressibleBlob(3 * chunk)
	writeAt(t, s, id, orig, 0) // committed baseline

	boom := errors.New("boom")
	err := s.Batch(ctx, id, func(w io.WriterAt) error {
		if _, err := w.WriteAt(bytes.Repeat([]byte("Z"), 2*chunk), 0); err != nil {
			return err
		}
		return boom // abort after writing
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Batch error = %v, want boom", err)
	}
	if got := readAll(t, s, id); !bytes.Equal(got, orig) {
		t.Fatal("Batch rollback did not restore the original content")
	}
	if sz, _ := s.Size(ctx, id); sz != int64(len(orig)) {
		t.Fatalf("size after rollback = %d, want %d", sz, len(orig))
	}
}

// TestBatchNotFound: Batch on a missing id returns ErrNotFound and never runs fn.
func TestBatchNotFound(t *testing.T) {
	s, _ := newStore(t)
	called := false
	err := s.Batch(context.Background(), 999, func(w io.WriterAt) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Batch on missing id = %v, want ErrNotFound", err)
	}
	if called {
		t.Fatal("fn ran for a missing object")
	}
}

// TestWriteAtFrom: copies a reader into an object at off 0 and at a sparse off.
func TestWriteAtFrom(t *testing.T) {
	skipUnderRace(t) // raw object uses OpenBlob
	const chunk = 32
	s, _ := newStore(t, WithChunkSize(chunk))
	ctx := context.Background()

	id, _ := s.Create(ctx)
	src := compressibleBlob(4*chunk + 5)
	n, err := s.WriteAtFrom(ctx, id, 0, bytes.NewReader(src))
	if err != nil || n != int64(len(src)) {
		t.Fatalf("WriteAtFrom = (%d, %v), want (%d, nil)", n, err, len(src))
	}
	if got := readAll(t, s, id); !bytes.Equal(got, src) {
		t.Fatal("WriteAtFrom content mismatch")
	}

	// At a non-zero offset the gap before it reads as zeros.
	id2, _ := s.Create(ctx)
	if _, err := s.WriteAtFrom(ctx, id2, int64(chunk), bytes.NewReader(src)); err != nil {
		t.Fatalf("WriteAtFrom at offset: %v", err)
	}
	want := append(make([]byte, chunk), src...)
	if got := readAll(t, s, id2); !bytes.Equal(got, want) {
		t.Fatalf("WriteAtFrom at offset: gap+content mismatch (got %d, want %d)", len(got), len(want))
	}
}

// TestConcurrentBatch: many goroutines each Batch-write a distinct compressed
// object with two WriteAt calls; all read back intact. Runs under -race.
func TestConcurrentBatch(t *testing.T) {
	s, _ := newStore(t, WithCompression(CompressionDefault), WithChunkSize(64))
	ctx := context.Background()
	const n = 12
	ids := make([]int64, n)
	payloads := make([][]byte, n)
	for i := range ids {
		id, _ := s.Create(ctx)
		ids[i] = id
		payloads[i] = compressibleBlob(200 + i*40)
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.Batch(ctx, ids[i], func(w io.WriterAt) error {
				half := len(payloads[i]) / 2
				if _, err := w.WriteAt(payloads[i][half:], int64(half)); err != nil {
					return err
				}
				_, err := w.WriteAt(payloads[i][:half], 0)
				return err
			})
		}(i)
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("batch %d: %v", i, errs[i])
		}
		if got := readAll(t, s, ids[i]); !bytes.Equal(got, payloads[i]) {
			t.Fatalf("object %d mismatch", i)
		}
	}
}

func TestOverwriteBelowSize(t *testing.T) {
	skipUnderRace(t)
	s, _ := newStore(t, WithChunkSize(8))
	ctx := context.Background()
	id, _ := s.Create(ctx)
	writeAt(t, s, id, []byte("0123456789ABCDEFGHIJ"), 0) // 20 bytes
	// Overwrite 4 bytes spanning the chunk-0/chunk-1 boundary; size unchanged.
	writeAt(t, s, id, []byte("####"), 8)
	if size, _ := s.Size(ctx, id); size != 20 {
		t.Fatalf("size = %d, want 20 (overwrite must not grow)", size)
	}
	if got := readAll(t, s, id); !bytes.Equal(got, []byte("01234567####CDEFGHIJ")) {
		t.Fatalf("overwrite = %q", got)
	}
}

func TestTruncateToZeroAndGrowSparse(t *testing.T) {
	skipUnderRace(t)
	s, _ := newStore(t, WithChunkSize(8))
	ctx := context.Background()
	id, _ := s.Create(ctx)
	writeAt(t, s, id, bytes.Repeat([]byte("z"), 30), 0)

	if err := s.Truncate(ctx, id, 0); err != nil {
		t.Fatalf("Truncate to 0: %v", err)
	}
	if size, _ := s.Size(ctx, id); size != 0 {
		t.Fatalf("size after truncate-0 = %d, want 0", size)
	}
	if got := readAll(t, s, id); len(got) != 0 {
		t.Fatalf("read after truncate-0 len = %d, want 0", len(got))
	}

	// Pure grow-via-Truncate is sparse: the whole range reads zero.
	if err := s.Truncate(ctx, id, 16); err != nil {
		t.Fatalf("Truncate grow: %v", err)
	}
	got := readAll(t, s, id)
	if len(got) != 16 {
		t.Fatalf("len = %d, want 16", len(got))
	}
	for i, b := range got {
		if b != 0 {
			t.Fatalf("byte %d = %d, want 0", i, b)
		}
	}
	// Truncate to the current size is a no-op.
	if err := s.Truncate(ctx, id, 16); err != nil {
		t.Fatalf("Truncate to same size: %v", err)
	}
	if size, _ := s.Size(ctx, id); size != 16 {
		t.Fatalf("size after no-op truncate = %d, want 16", size)
	}
}

func TestEmptyAndNegativeArgs(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	id, _ := s.Create(ctx)

	w, err := s.Writer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if n, err := w.WriteAt(nil, 0); n != 0 || err != nil {
		t.Fatalf("empty WriteAt = (%d, %v)", n, err)
	}
	if size, _ := s.Size(ctx, id); size != 0 {
		t.Fatalf("empty WriteAt changed size to %d", size)
	}
	if _, err := w.WriteAt([]byte("x"), -1); err == nil {
		t.Error("WriteAt(off=-1): want error")
	}

	r, err := s.Reader(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if n, err := r.ReadAt([]byte{}, 0); n != 0 || err != nil {
		t.Fatalf("empty ReadAt = (%d, %v)", n, err)
	}
	if _, err := r.ReadAt(make([]byte, 1), -1); err == nil {
		t.Error("ReadAt(off=-1): want error")
	}
	if err := s.Truncate(ctx, id, -1); err == nil {
		t.Error("Truncate(size=-1): want error")
	}
}

// TestInvalidChunkGuard verifies a row with a non-positive chunk size (which
// only a foreign writer could create — our own CHECK forbids it) yields an
// error rather than an integer divide-by-zero panic. The tables are
// pre-created WITHOUT the CHECK so the poison row can be inserted.
func TestInvalidChunkGuard(t *testing.T) {
	db, err := sqlite.OpenWAL(filepath.Join(t.TempDir(), "poison.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	for _, ddl := range []string{
		`CREATE TABLE files_objects(id INTEGER PRIMARY KEY, size INTEGER NOT NULL DEFAULT 0, chunk INTEGER NOT NULL)`,
		`CREATE TABLE files_chunks(id INTEGER PRIMARY KEY, obj INTEGER NOT NULL, seq INTEGER NOT NULL, data BLOB NOT NULL)`,
		`CREATE UNIQUE INDEX files_chunks_obj_seq ON files_chunks(obj, seq)`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(db, "files") // reuses the no-CHECK tables (IF NOT EXISTS)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.ExecContext(ctx, `INSERT INTO files_objects(size, chunk) VALUES (10, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	w, _ := s.Writer(ctx, id)
	if n, err := w.WriteAt([]byte("x"), 0); err == nil {
		t.Error("WriteAt on chunk=0 row: want error, got nil (panic?)")
	} else if n != 0 {
		t.Errorf("WriteAt error path returned n=%d, want 0 (nothing persisted)", n)
	}
	r, _ := s.Reader(ctx, id)
	if _, err := r.ReadAt(make([]byte, 1), 0); err == nil {
		t.Error("ReadAt on chunk=0 row: want error")
	}
	if err := s.Truncate(ctx, id, 5); err == nil {
		t.Error("Truncate on chunk=0 row: want error")
	}

	// A chunk size past the ceiling is rejected before any allocation, rather
	// than driving a multi-GiB make/Grow.
	res, err = db.ExecContext(ctx, `INSERT INTO files_objects(size, chunk) VALUES (10, ?)`, int64(maxChunkSize)+1)
	if err != nil {
		t.Fatal(err)
	}
	big, _ := res.LastInsertId()
	rb, _ := s.Reader(ctx, big)
	if _, err := rb.ReadAt(make([]byte, 1), 0); err == nil {
		t.Error("ReadAt on oversized-chunk row: want error")
	}
}

// TestChunkSizeBounds: Open rejects a non-positive or oversized chunk size.
func TestChunkSizeBounds(t *testing.T) {
	db, err := sqlite.OpenWAL(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := Open(db, "files", WithChunkSize(0)); err == nil {
		t.Error("Open with zero chunk size: want error")
	}
	if _, err := Open(db, "files", WithChunkSize(maxChunkSize+1)); err == nil {
		t.Error("Open with oversized chunk size: want error")
	}
}

func TestSameIDConcurrentWriters(t *testing.T) {
	skipUnderRace(t)
	s, _ := newStore(t, WithChunkSize(16))
	ctx := context.Background()
	id, _ := s.Create(ctx)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w, err := s.Writer(ctx, id)
			if err != nil {
				errs[i] = err
				return
			}
			defer w.Close()
			region := bytes.Repeat([]byte{byte('A' + i)}, 10) // disjoint 10-byte slot
			if _, err := w.WriteAt(region, int64(i*10)); err != nil {
				errs[i] = err
			}
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
	}
	// BEGIN IMMEDIATE serializes the writers; the size RMW (max(size,?)) never
	// regresses and every disjoint region survives.
	if size, _ := s.Size(ctx, id); size != int64(n*10) {
		t.Fatalf("size = %d, want %d", size, n*10)
	}
	got := readAll(t, s, id)
	for i := range n {
		if want := bytes.Repeat([]byte{byte('A' + i)}, 10); !bytes.Equal(got[i*10:(i+1)*10], want) {
			t.Fatalf("region %d = %q, want %q", i, got[i*10:(i+1)*10], want)
		}
	}
}

// TestWriteVsConvertConcurrent guards the A1 fix: WriteAt reads the object's
// codec INSIDE its write transaction, so a concurrent SetCompression mode
// conversion can never make a writer store a chunk in the wrong representation.
// Each writer owns a disjoint byte region, so the final content is deterministic
// regardless of interleaving and of mode toggles — conversion preserves content
// and BEGIN IMMEDIATE serializes the writers. A converter flips the object
// between raw and compressed underneath them; the object must stay fully
// readable (no decode error) with every region intact. With the bug (codec read
// before BEGIN) a stale-mode write corrupts a chunk and a region read fails.
func TestWriteVsConvertConcurrent(t *testing.T) {
	skipUnderRace(t) // writers hit the raw OpenBlob path in raw phases
	s, _ := newStore(t, WithChunkSize(16))
	ctx := context.Background()
	id, _ := s.Create(ctx, WithObjectCompression(CompressionBest))

	const writers = 4
	regions := make([][]byte, writers)
	for i := range regions {
		regions[i] = bytes.Repeat([]byte{byte('A' + i)}, 10) // disjoint 10-byte slot
	}

	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for r := 0; r < 8; r++ { // re-write idempotently to widen the race window
				w, err := s.Writer(ctx, id)
				if err != nil {
					errs[i] = err
					return
				}
				_, err = w.WriteAt(regions[i], int64(i*10))
				w.Close()
				if err != nil {
					errs[i] = err
					return
				}
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		levels := []Compression{CompressionNone, CompressionBest}
		for r := 0; r < 16; r++ {
			// A conversion error (e.g. busy) leaves the object consistent in
			// whatever mode it is in; the content checks below still hold.
			_ = s.SetCompression(ctx, id, levels[r%2])
		}
	}()
	wg.Wait()

	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
	}
	got := readAll(t, s, id) // surfaces any undecodable chunk as a fatal error
	if int64(len(got)) < int64(writers*10) {
		t.Fatalf("short read: got %d bytes, want >= %d", len(got), writers*10)
	}
	for i := range writers {
		if want := regions[i]; !bytes.Equal(got[i*10:(i+1)*10], want) {
			t.Fatalf("region %d = %q, want %q (stale-codec corruption?)", i, got[i*10:(i+1)*10], want)
		}
	}
	if _, err := s.Stat(ctx, id); err != nil {
		t.Fatalf("Stat after concurrent convert: %v", err)
	}
}

func pragmaIntDB(t *testing.T, db *sqlite.DB, pragma string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(&n); err != nil {
		t.Fatalf("PRAGMA %s: %v", pragma, err)
	}
	return n
}

func TestVacuumOnDelete(t *testing.T) {
	skipUnderRace(t)
	db, err := sqlite.Open(sqlite.Config{
		Path:    filepath.Join(t.TempDir(), "vac.db"),
		Pragmas: sqlite.Pragmas{JournalMode: sqlite.JournalWAL, AutoVacuum: sqlite.AutoVacuumIncremental},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	s, err := Open(db, "files", WithVacuumOnDelete())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.Create(ctx)
	writeAt(t, s, id, bytes.Repeat([]byte("x"), 200<<10), 0) // 200 KiB

	before := pragmaIntDB(t, db, "page_count")
	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if after := pragmaIntDB(t, db, "page_count"); after >= before {
		t.Fatalf("vacuum-on-delete did not shrink the file: page_count before=%d after=%d", before, after)
	}
	if free := pragmaIntDB(t, db, "freelist_count"); free != 0 {
		t.Fatalf("freelist_count = %d after vacuum-on-delete, want 0", free)
	}
}
