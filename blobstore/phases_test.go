package blobstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// hashedBlockCount is the number of blocks carrying a dedup content hash.
func hashedBlockCount(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM files_blocks WHERE hash IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// readVersion returns the full content of version vno of object obj.
func readVersion(t *testing.T, s *Store, obj, vno int64) []byte {
	t.Helper()
	ctx := context.Background()
	var size int64 = -1
	vs, err := s.ListVersions(ctx, obj)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	for _, v := range vs {
		if v.VersionNo == vno {
			size = v.Size
		}
	}
	if size < 0 {
		t.Fatalf("version %d not listed for object %d", vno, obj)
	}
	r, err := s.OpenVersion(ctx, obj, vno)
	if err != nil {
		t.Fatalf("OpenVersion: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(io.NewSectionReader(r, 0, size))
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	return got
}

// --- Phase 2: Clone + Stat sharing ----------------------------------------

func TestCloneSharesAndStat(t *testing.T) {
	s, db := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault))
	ctx := context.Background()
	a, _ := s.Create(ctx)
	writeAt(t, s, a, bytes.Repeat([]byte("z"), 128), 0) // two chunks

	pre, _ := s.Stat(ctx, a)
	if pre.SharedBytes != 0 || pre.UniqueBytes == 0 || pre.StoredBytes != pre.UniqueBytes {
		t.Fatalf("before clone: unique=%d shared=%d stored=%d, want shared=0 stored=unique>0",
			pre.UniqueBytes, pre.SharedBytes, pre.StoredBytes)
	}

	b := mustClone(t, s, a)
	if got := blockCount(t, db); got != 2 {
		t.Fatalf("clone copied bytes: blocks=%d, want 2", got)
	}
	if got := readAll(t, s, b); !bytes.Equal(got, readAll(t, s, a)) {
		t.Fatal("clone content differs from source")
	}
	for _, id := range []int64{a, b} {
		info, _ := s.Stat(ctx, id)
		if info.UniqueBytes != 0 || info.SharedBytes == 0 || info.StoredBytes != info.SharedBytes {
			t.Fatalf("after clone obj %d: unique=%d shared=%d stored=%d, want unique=0 stored=shared>0",
				id, info.UniqueBytes, info.SharedBytes, info.StoredBytes)
		}
	}
}

func TestCloneNotFound(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Clone(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Clone(missing) = %v, want ErrNotFound", err)
	}
}

// --- Phase 3: read-only / no-DDL open -------------------------------------

func TestOpenReadOnly(t *testing.T) {
	s, db := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault))
	ctx := context.Background()
	a, _ := s.Create(ctx)
	writeAt(t, s, a, []byte("payload"), 0)

	ro, err := OpenReadOnly(db, "files")
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	// Reads work.
	if got := readAll(t, ro, a); string(got) != "payload" {
		t.Fatalf("read-only read = %q, want payload", got)
	}
	if _, err := ro.Stat(ctx, a); err != nil {
		t.Fatalf("read-only Stat: %v", err)
	}
	// Every mutation is refused.
	if _, err := ro.Create(ctx); !errors.Is(err, ErrReadOnly) {
		t.Errorf("ro.Create = %v, want ErrReadOnly", err)
	}
	if err := ro.Delete(ctx, a); !errors.Is(err, ErrReadOnly) {
		t.Errorf("ro.Delete = %v, want ErrReadOnly", err)
	}
	if err := ro.Truncate(ctx, a, 0); !errors.Is(err, ErrReadOnly) {
		t.Errorf("ro.Truncate = %v, want ErrReadOnly", err)
	}
	if _, err := ro.Clone(ctx, a); !errors.Is(err, ErrReadOnly) {
		t.Errorf("ro.Clone = %v, want ErrReadOnly", err)
	}
	if _, err := ro.NewVersion(ctx, a); !errors.Is(err, ErrReadOnly) {
		t.Errorf("ro.NewVersion = %v, want ErrReadOnly", err)
	}
	if _, err := ro.WriteAtFrom(ctx, a, 0, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrReadOnly) {
		t.Errorf("ro.WriteAtFrom = %v, want ErrReadOnly", err)
	}
	if err := ro.Batch(ctx, a, func(io.WriterAt) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Errorf("ro.Batch = %v, want ErrReadOnly", err)
	}
	if err := ro.SetCompression(ctx, a, CompressionBest); !errors.Is(err, ErrReadOnly) {
		t.Errorf("ro.SetCompression = %v, want ErrReadOnly", err)
	}
	if err := ro.SetRetention(ctx, a, Policy{KeepVersions: 1}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("ro.SetRetention = %v, want ErrReadOnly", err)
	}
	if err := ro.Prune(ctx, a); !errors.Is(err, ErrReadOnly) {
		t.Errorf("ro.Prune = %v, want ErrReadOnly", err)
	}
	// Writer is handed out (it only confirms existence) but its writes are refused.
	w, err := ro.Writer(ctx, a)
	if err != nil {
		t.Fatalf("ro.Writer (read path): %v", err)
	}
	if _, err := w.WriteAt([]byte("x"), 0); !errors.Is(err, ErrReadOnly) {
		t.Errorf("ro Writer.WriteAt = %v, want ErrReadOnly", err)
	}
}

func TestOpenReadOnlyNotProvisioned(t *testing.T) {
	_, db := newStore(t) // provisions "files", not "ghost"
	if _, err := OpenReadOnly(db, "ghost"); err == nil {
		t.Fatal("OpenReadOnly of an un-provisioned store: want error")
	}
}

// --- Phase 4: versioning + retention --------------------------------------

func TestVersioningBasic(t *testing.T) {
	s, db := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault))
	ctx := context.Background()
	a, _ := s.Create(ctx)

	writeAt(t, s, a, bytes.Repeat([]byte("1"), 64), 0)
	v1, err := s.NewVersion(ctx, a)
	if err != nil || v1 != 1 {
		t.Fatalf("NewVersion 1 = (%d, %v)", v1, err)
	}
	preDiverge := blockCount(t, db)

	// Overwrite, snapshot again.
	writeAt(t, s, a, bytes.Repeat([]byte("2"), 64), 0)
	if blockCount(t, db) <= preDiverge {
		t.Fatal("divergent write did not allocate a new block (version not isolated)")
	}
	v2, err := s.NewVersion(ctx, a)
	if err != nil || v2 != 2 {
		t.Fatalf("NewVersion 2 = (%d, %v)", v2, err)
	}

	if vs, _ := s.ListVersions(ctx, a); len(vs) != 2 {
		t.Fatalf("ListVersions = %d entries, want 2", len(vs))
	}
	if got := readVersion(t, s, a, 1); !bytes.Equal(got, bytes.Repeat([]byte("1"), 64)) {
		t.Fatal("version 1 content drifted after the live object changed")
	}
	if got := readVersion(t, s, a, 2); !bytes.Equal(got, bytes.Repeat([]byte("2"), 64)) {
		t.Fatal("version 2 content wrong")
	}
	if got := readAll(t, s, a); !bytes.Equal(got, bytes.Repeat([]byte("2"), 64)) {
		t.Fatal("live object content wrong")
	}
}

func TestVersioningKeepN(t *testing.T) {
	s, _ := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault))
	ctx := context.Background()
	a, _ := s.Create(ctx, WithObjectVersioning(Policy{KeepVersions: 2}))

	for i := range 4 {
		writeAt(t, s, a, bytes.Repeat([]byte{byte('a' + i)}, 64), 0)
		if _, err := s.NewVersion(ctx, a); err != nil {
			t.Fatalf("NewVersion %d: %v", i, err)
		}
	}
	vs, _ := s.ListVersions(ctx, a)
	if len(vs) != 2 {
		t.Fatalf("keep-2 retained %d versions, want 2", len(vs))
	}
	if vs[0].VersionNo != 3 || vs[1].VersionNo != 4 {
		t.Fatalf("retained versions = %d,%d, want 3,4", vs[0].VersionNo, vs[1].VersionNo)
	}
	if _, err := s.OpenVersion(ctx, a, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("evicted version 1 still opens: %v", err)
	}
}

func TestVersioningMaxAge(t *testing.T) {
	s, _ := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault))
	ctx := context.Background()
	clk := time.Unix(1_000_000, 0)
	s.now = func() time.Time { return clk }

	a, _ := s.Create(ctx)
	if err := s.SetRetention(ctx, a, Policy{MaxAge: time.Hour}); err != nil {
		t.Fatalf("SetRetention: %v", err)
	}
	writeAt(t, s, a, bytes.Repeat([]byte("1"), 64), 0)
	if _, err := s.NewVersion(ctx, a); err != nil {
		t.Fatalf("NewVersion 1: %v", err)
	}

	clk = clk.Add(2 * time.Hour) // age version 1 past the bound
	writeAt(t, s, a, bytes.Repeat([]byte("2"), 64), 0)
	if _, err := s.NewVersion(ctx, a); err != nil { // its trailing sweep prunes v1
		t.Fatalf("NewVersion 2: %v", err)
	}
	vs, _ := s.ListVersions(ctx, a)
	if len(vs) != 1 || vs[0].VersionNo != 2 {
		t.Fatalf("max-age kept %v, want only version 2", vs)
	}
}

func TestVersioningPruneFreesBytes(t *testing.T) {
	s, db := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault))
	ctx := context.Background()
	a, _ := s.Create(ctx)

	writeAt(t, s, a, bytes.Repeat([]byte("1"), 64), 0)
	if _, err := s.NewVersion(ctx, a); err != nil { // v1 snapshots block("1")
		t.Fatal(err)
	}
	writeAt(t, s, a, bytes.Repeat([]byte("2"), 64), 0) // diverge: v1 alone holds block("1")
	if _, err := s.NewVersion(ctx, a); err != nil {    // v2 snapshots block("2")
		t.Fatal(err)
	}
	if got := blockCount(t, db); got != 2 {
		t.Fatalf("before prune: blocks=%d, want 2", got)
	}

	// Keep only the newest version and prune: v1 (and its uniquely-held block) goes.
	if err := s.SetRetention(ctx, a, Policy{KeepVersions: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(ctx, a); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got := blockCount(t, db); got != 1 {
		t.Fatalf("after prune: blocks=%d, want 1 (v1's block freed)", got)
	}
	if vs, _ := s.ListVersions(ctx, a); len(vs) != 1 || vs[0].VersionNo != 2 {
		t.Fatalf("prune left %v, want only version 2", vs)
	}

	// Deleting the object frees its remaining version snapshot too.
	if err := s.Delete(ctx, a); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := blockCount(t, db); got != 0 {
		t.Fatalf("after deleting a versioned object, blocks=%d, want 0", got)
	}
}

func TestVersioningReadOnlyOpen(t *testing.T) {
	s, db := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault))
	ctx := context.Background()
	a, _ := s.Create(ctx)
	writeAt(t, s, a, bytes.Repeat([]byte("v"), 64), 0)
	if _, err := s.NewVersion(ctx, a); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(db, "files")
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	if vs, _ := ro.ListVersions(ctx, a); len(vs) != 1 {
		t.Fatalf("read-only ListVersions = %d, want 1", len(vs))
	}
	if got := readVersion(t, ro, a, 1); !bytes.Equal(got, bytes.Repeat([]byte("v"), 64)) {
		t.Fatal("read-only version read wrong")
	}
}

// --- Phase 5: content-addressed dedup -------------------------------------

func TestDedupSharesIdenticalContent(t *testing.T) {
	s, db := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault), WithDedup())
	ctx := context.Background()
	a, _ := s.Create(ctx)
	b, _ := s.Create(ctx)

	content := bytes.Repeat([]byte("dup"), 64)[:64]
	writeAt(t, s, a, content, 0)
	writeAt(t, s, b, content, 0) // identical → should reference a's block

	if got := blockCount(t, db); got != 1 {
		t.Fatalf("dedup of identical content: blocks=%d, want 1", got)
	}
	ba, ra := chunkBlock(t, db, a, 0)
	bb, rb := chunkBlock(t, db, b, 0)
	if ba != bb || ra != 2 || rb != 2 {
		t.Fatalf("not deduped: a=(%d,%d) b=(%d,%d)", ba, ra, bb, rb)
	}
	if got := readAll(t, s, b); !bytes.Equal(got, content) {
		t.Fatal("deduped read wrong")
	}
}

func TestDedupDivergeOnWrite(t *testing.T) {
	s, db := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault), WithDedup())
	ctx := context.Background()
	a, _ := s.Create(ctx)
	b, _ := s.Create(ctx)
	content := bytes.Repeat([]byte("dup"), 64)[:64]
	writeAt(t, s, a, content, 0)
	writeAt(t, s, b, content, 0)

	writeAt(t, s, b, bytes.Repeat([]byte("q"), 64), 0) // b now differs
	if got := blockCount(t, db); got != 2 {
		t.Fatalf("after divergence: blocks=%d, want 2", got)
	}
	if got := readAll(t, s, a); !bytes.Equal(got, content) {
		t.Fatal("dedup source corrupted by other object's write")
	}
}

func TestDedupRawInPlaceClearsHash(t *testing.T) {
	skipUnderRace(t) // in-place raw write exercises modernc's BLOB I/O (checkptr)
	s, db := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault), WithDedup())
	ctx := context.Background()
	a, _ := s.Create(ctx)
	b, _ := s.Create(ctx)
	content := bytes.Repeat([]byte("k"), 64)
	writeAt(t, s, a, content, 0)
	writeAt(t, s, b, content, 0) // shared compressed block, hashed

	// Convert a to raw: its chunk is rewritten as a verbatim (hashed) block.
	if err := s.SetCompression(ctx, a, CompressionNone); err != nil {
		t.Fatalf("SetCompression: %v", err)
	}
	if got := hashedBlockCount(t, s); got != 2 {
		t.Fatalf("after convert, hashed blocks=%d, want 2", got)
	}

	// An in-place raw write must drop the hash so the mutated bytes can never be
	// matched by a stale index entry.
	writeAt(t, s, a, bytes.Repeat([]byte("Z"), 8), 0)
	if got := hashedBlockCount(t, s); got != 1 {
		t.Fatalf("in-place write left a stale hash: hashed blocks=%d, want 1", got)
	}
	want := append(bytes.Repeat([]byte("Z"), 8), bytes.Repeat([]byte("k"), 56)...)
	if got := readAll(t, s, a); !bytes.Equal(got, want) {
		t.Fatalf("raw in-place write wrong: %q", got)
	}
	if got := readAll(t, s, b); !bytes.Equal(got, content) {
		t.Fatal("other object corrupted by in-place write")
	}
	_ = db
}

// TestDedupIntraObjectRefcount: when one object maps several chunks to a single
// deduped block, releasing some of those chunks must decrement the block's
// refcount by how many it dropped — not by one — or the block leaks. Exercises
// the multiplicity-correct decrement in releaseChunks for both Truncate-shrink
// and Delete.
func TestDedupIntraObjectRefcount(t *testing.T) {
	s, db := newStore(t, WithChunkSize(64), WithCompression(CompressionDefault), WithDedup())
	ctx := context.Background()
	a, _ := s.Create(ctx)
	writeAt(t, s, a, bytes.Repeat([]byte("m"), 192), 0) // three identical chunks → one block
	if got := blockCount(t, db); got != 1 {
		t.Fatalf("intra-object dedup: blocks=%d, want 1", got)
	}
	if _, r := chunkBlock(t, db, a, 0); r != 3 {
		t.Fatalf("shared block refs=%d, want 3", r)
	}

	if err := s.Truncate(ctx, a, 64); err != nil { // drop two of the three chunks
		t.Fatalf("truncate: %v", err)
	}
	if got := blockCount(t, db); got != 1 {
		t.Fatalf("after truncate: blocks=%d, want 1 (block still held by chunk0)", got)
	}
	if _, r := chunkBlock(t, db, a, 0); r != 1 {
		t.Fatalf("after truncate refs=%d, want 1 (decremented by 2, not 1)", r)
	}

	if err := s.Delete(ctx, a); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := blockCount(t, db); got != 0 {
		t.Fatalf("after delete: blocks=%d, want 0 (no leak)", got)
	}
}
