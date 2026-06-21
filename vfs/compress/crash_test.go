package compress

// Increment-3 crash-injection tests — the make-or-break for the live VFS.
//
// crashBacking is an in-memory backing that models a power loss: a "crash"
// drops every write issued since the last successful fsync (only Sync'd bytes
// are durable). Injecting a crash at each step of the commit protocol and
// reopening over the durable image proves the protocol's invariant: after a
// crash at ANY point, the container reopens to a fully consistent committed
// generation — the previous one or the new one, never a torn mix.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

var errSimCrash = errors.New("simulated crash")

// crashBacking is an in-memory [backing] with a fault point. data is the
// working image; synced is the durable image as of the last successful Sync.
// When ops reaches failAt, the operation "crashes" — it is not applied and
// returns errSimCrash — modelling a power loss at that instant.
type crashBacking struct {
	data   []byte
	synced []byte
	ops    int
	failAt int // 0 ⇒ never fail
}

// newCrashBacking starts from a durable image (nil for an empty file).
func newCrashBacking(initial []byte) *crashBacking {
	return &crashBacking{
		data:   append([]byte(nil), initial...),
		synced: append([]byte(nil), initial...),
	}
}

func (c *crashBacking) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(c.data)) {
		return 0, io.EOF
	}
	n := copy(p, c.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (c *crashBacking) WriteAt(p []byte, off int64) (int, error) {
	c.ops++
	if c.failAt != 0 && c.ops >= c.failAt {
		return 0, errSimCrash
	}
	if end := off + int64(len(p)); end > int64(len(c.data)) {
		c.data = append(c.data, make([]byte, end-int64(len(c.data)))...)
	}
	copy(c.data[off:], p)
	return len(p), nil
}

func (c *crashBacking) Sync() error {
	c.ops++
	if c.failAt != 0 && c.ops >= c.failAt {
		return errSimCrash // crash before this fsync makes anything durable
	}
	c.synced = append([]byte(nil), c.data...)
	return nil
}

func (c *crashBacking) Size() (int64, error) { return int64(len(c.data)), nil }
func (c *crashBacking) Close() error         { return nil }

// constPage returns a page-sized buffer filled with b.
func constPage(b byte, pageSize int) []byte {
	p := make([]byte, pageSize)
	for i := range p {
		p[i] = b
	}
	return p
}

// readAllLogical reads the whole logical database content of f.
func readAllLogical(t *testing.T, f *mainFile) []byte {
	t.Helper()
	sz, err := f.Size()
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	buf := make([]byte, sz)
	if sz == 0 {
		return buf
	}
	if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
		t.Fatalf("ReadAt: %v", err)
	}
	return buf
}

const crashPageSize = 4096 // small pages keep the test fast

// applyTxn rewrites page 1 and appends page 3 — the transaction whose commit we
// crash at every step.
func applyTxn(f *mainFile) {
	_, _ = f.WriteAt(constPage(0x21, crashPageSize), 1*crashPageSize)
	_, _ = f.WriteAt(constPage(0x23, crashPageSize), 3*crashPageSize)
	_ = f.Sync(0)
}

func TestCommitCrashAtEveryStep(t *testing.T) {
	open := func(cb *crashBacking) *mainFile {
		t.Helper()
		f, err := openMainOver(cb, false, defaultBlockSize, crashPageSize, CompressionDefault)
		if err != nil {
			t.Fatalf("openMainOver: %v", err)
		}
		return f
	}

	// Build the committed "before" image: pages 0,1,2.
	base := newCrashBacking(nil)
	f := open(base)
	_, _ = f.WriteAt(constPage(0x10, crashPageSize), 0*crashPageSize)
	_, _ = f.WriteAt(constPage(0x11, crashPageSize), 1*crashPageSize)
	_, _ = f.WriteAt(constPage(0x12, crashPageSize), 2*crashPageSize)
	if err := f.Sync(0); err != nil {
		t.Fatalf("commit before: %v", err)
	}
	beforeImg := append([]byte(nil), base.synced...)
	beforeContent := readAllLogical(t, f)

	// Compute the "after" content with a clean run from the before image.
	clean := newCrashBacking(beforeImg)
	fc := open(clean)
	applyTxn(fc)
	afterContent := readAllLogical(t, fc)
	totalOps := clean.ops // every backing op in one transaction + its commit
	if bytes.Equal(beforeContent, afterContent) {
		t.Fatal("before and after content are identical; the transaction did nothing")
	}

	sawBefore, sawAfter := false, false
	// Crash at each op of the transaction+commit (and one step past, = clean).
	for k := 1; k <= totalOps+1; k++ {
		cb := newCrashBacking(beforeImg)
		fk := open(cb)
		cb.failAt = k // open did only reads, so ops counts from the transaction
		applyTxn(fk)  // a crash here is expected and ignored
		_ = fk.Close()

		// Reopen over the durable image and verify a consistent committed state.
		rec := newCrashBacking(cb.synced)
		fr, err := openMainOver(rec, false, defaultBlockSize, crashPageSize, CompressionDefault)
		if err != nil {
			t.Fatalf("crash at op %d: reopen failed: %v", k, err)
		}
		got := readAllLogical(t, fr)
		_ = fr.Close()
		switch {
		case bytes.Equal(got, beforeContent):
			sawBefore = true
		case bytes.Equal(got, afterContent):
			sawAfter = true
		default:
			t.Fatalf("crash at op %d: recovered state is neither the committed before nor after image (TORN)", k)
		}
	}
	if !sawBefore {
		t.Error("no crash point recovered the previous committed state")
	}
	if !sawAfter {
		t.Error("the clean run did not reach the new committed state")
	}
}

func TestTornSuperblockFallsBackToPrevGen(t *testing.T) {
	cb := newCrashBacking(nil)
	f, err := openMainOver(cb, false, defaultBlockSize, crashPageSize, CompressionDefault)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Generation A: page 0 = 0xA0.
	_, _ = f.WriteAt(constPage(0xA0, crashPageSize), 0)
	if err := f.Sync(0); err != nil {
		t.Fatalf("commit A: %v", err)
	}
	genAContent := readAllLogical(t, f)
	// Generation B: page 0 = 0xB0.
	_, _ = f.WriteAt(constPage(0xB0, crashPageSize), 0)
	if err := f.Sync(0); err != nil {
		t.Fatalf("commit B: %v", err)
	}
	genBContent := readAllLogical(t, f)
	if bytes.Equal(genAContent, genBContent) {
		t.Fatal("gen A and B identical")
	}

	// Corrupt the newer superblock (a torn block write) — flip a byte inside its
	// CRC-protected region. Reopen must fall back to the previous generation.
	img := append([]byte(nil), cb.synced...)
	off := latestSuperblockOffset(t, img)
	img[off+30] ^= 0xFF

	rec := newCrashBacking(img)
	fr, err := openMainOver(rec, false, defaultBlockSize, crashPageSize, CompressionDefault)
	if err != nil {
		t.Fatalf("reopen after torn superblock: %v", err)
	}
	got := readAllLogical(t, fr)
	_ = fr.Close()
	if !bytes.Equal(got, genAContent) {
		t.Fatalf("after torn newer superblock, recovered state is not generation A")
	}
}

func TestDirectoryCorruptionRejected(t *testing.T) {
	cb := newCrashBacking(nil)
	f, err := openMainOver(cb, false, defaultBlockSize, crashPageSize, CompressionDefault)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = f.WriteAt(constPage(0x55, crashPageSize), 0)
	if err := f.Sync(0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	dirOffset := f.c.committedDirOffset
	if dirOffset == 0 {
		t.Fatal("expected a non-empty directory")
	}

	// Corrupt a byte of the committed directory (superblock stays valid).
	img := append([]byte(nil), cb.synced...)
	img[dirOffset] ^= 0xFF

	rec := newCrashBacking(img)
	_, err = openMainOver(rec, false, defaultBlockSize, crashPageSize, CompressionDefault)
	if err == nil {
		t.Fatal("reopen accepted a corrupted directory; want a checksum error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("directory checksum")) {
		t.Fatalf("error = %v, want a directory checksum mismatch", err)
	}
}

// latestSuperblockOffset returns the byte offset of the higher-generation valid
// superblock in img (slot 0 at offset 0, slot 1 at offset blockSize).
func latestSuperblockOffset(t *testing.T, img []byte) int64 {
	t.Helper()
	a := make([]byte, superblockSize)
	b := make([]byte, superblockSize)
	copy(a, img)
	if int64(len(img)) > int64(defaultBlockSize) {
		copy(b, img[defaultBlockSize:])
	}
	sa, ea := parseSuperblock(a)
	sb, eb := parseSuperblock(b)
	switch {
	case ea == nil && eb == nil:
		if sb.generation > sa.generation {
			return int64(defaultBlockSize)
		}
		return 0
	case eb == nil:
		return int64(defaultBlockSize)
	default:
		return 0
	}
}

// TestLiveRecoversFromCorruptLatestSuperblock is the end-to-end recovery proof:
// a real database through OpenLive whose newest superblock is corrupted reopens
// at the previous committed transaction, intact and verifiable.
func TestLiveRecoversFromCorruptLatestSuperblock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recover.dbz")

	db := openLive(t, path, Options{})
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	const rows = 20
	for i := range rows {
		if _, err := db.Exec(`INSERT INTO t (k) VALUES (?)`, i); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Corrupt the newest superblock on disk: the last committed transaction (the
	// final insert) becomes unreachable, and reopen must fall back one commit.
	corruptLatestSuperblockOnDisk(t, path)

	db = openLive(t, path, Options{})
	defer db.Close()
	var ic string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check = (%q, %v), want ok", ic, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != rows-1 {
		t.Fatalf("count = %d, want %d (recovered to the previous committed transaction)", n, rows-1)
	}
}

func corruptLatestSuperblockOnDisk(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open for corruption: %v", err)
	}
	defer f.Close()
	a := make([]byte, superblockSize)
	b := make([]byte, superblockSize)
	_, _ = f.ReadAt(a, 0)
	_, _ = f.ReadAt(b, int64(defaultBlockSize))
	sa, ea := parseSuperblock(a)
	sb, eb := parseSuperblock(b)
	off := int64(0)
	if eb == nil && (ea != nil || sb.generation > sa.generation) {
		off = int64(defaultBlockSize)
	}
	cur := make([]byte, 1)
	if _, err := f.ReadAt(cur, off+30); err != nil {
		t.Fatalf("read superblock byte: %v", err)
	}
	cur[0] ^= 0xFF
	if _, err := f.WriteAt(cur, off+30); err != nil {
		t.Fatalf("corrupt superblock: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync corruption: %v", err)
	}
}

var _ backing = (*crashBacking)(nil)
