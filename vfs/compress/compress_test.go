package compress

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlite "gosqlite.org"
)

// header returns the first n bytes of the file at path (or fewer if shorter).
func header(t *testing.T, path string, n int) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %q: %v", path, err)
	}
	defer f.Close()
	b := make([]byte, n)
	m, _ := f.Read(b)
	return b[:m]
}

func isCompressed(t *testing.T, path string) bool {
	t.Helper()
	return !looksLikeSQLite(header(t, path, len(sqliteMagic)))
}

func mustExec(t *testing.T, db *sqlite.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestRoundTrip(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "app.db.az")

	db, err := Open(sqlite.Config{Path: dest}, Options{Level: CompressionBest})
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	mustExec(t, db, `CREATE TABLE t (k TEXT PRIMARY KEY, v TEXT)`)
	mustExec(t, db, `INSERT INTO t VALUES ('hello', 'compressed world')`)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("compressed file not written: %v", err)
	}
	if !isCompressed(t, dest) {
		t.Fatal("on-disk file is a raw SQLite database, want compressed")
	}

	db2, err := Open(sqlite.Config{Path: dest}, Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	var v string
	if err := db2.QueryRow(`SELECT v FROM t WHERE k = 'hello'`).Scan(&v); err != nil {
		t.Fatalf("query: %v", err)
	}
	if v != "compressed world" {
		t.Fatalf("got %q, want %q", v, "compressed world")
	}
}

func TestCompressionRatio(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "ratio.db.az")
	row := strings.Repeat("abcdefgh ", 100) // ~900 B, very compressible
	const rows = 1000
	logical := int64(len(row)) * rows

	db, err := Open(sqlite.Config{Path: dest}, Options{Level: CompressionDefault})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustExec(t, db, `CREATE TABLE t (v TEXT)`)
	for range rows {
		mustExec(t, db, `INSERT INTO t VALUES (?)`, row)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= logical/4 {
		t.Fatalf("compressed file %d bytes is not meaningfully smaller than logical %d", info.Size(), logical)
	}
}

func TestAdoptRawDatabase(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "raw.db")

	// Create a normal, uncompressed database first.
	raw, err := sqlite.Open(sqlite.Config{Path: dest})
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	mustExec(t, raw, `CREATE TABLE t (v INTEGER)`)
	mustExec(t, raw, `INSERT INTO t VALUES (42)`)
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	if isCompressed(t, dest) {
		t.Fatal("precondition: file should be a raw SQLite database")
	}

	// Opening it with compress.Open adopts it; Close rewrites it compressed.
	db, err := Open(sqlite.Config{Path: dest}, Options{})
	if err != nil {
		t.Fatalf("adopt open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("adopt close: %v", err)
	}
	if !isCompressed(t, dest) {
		t.Fatal("adopted file was not compressed on close")
	}

	db2, err := Open(sqlite.Config{Path: dest}, Options{})
	if err != nil {
		t.Fatalf("reopen adopted: %v", err)
	}
	defer db2.Close()
	var v int
	if err := db2.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil || v != 42 {
		t.Fatalf("got (%d, %v), want (42, nil)", v, err)
	}
}

func TestReadOnlySkipsRecompress(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "ro.db.az")
	db, err := Open(sqlite.Config{Path: dest}, Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustExec(t, db, `CREATE TABLE t (v INTEGER)`)
	mustExec(t, db, `INSERT INTO t VALUES (7)`)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	before, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}

	ro, err := Open(sqlite.Config{Path: dest, Mode: sqlite.ModeReadOnly}, Options{})
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	var v int
	if err := ro.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil || v != 7 {
		t.Fatalf("ro read got (%d, %v), want (7, nil)", v, err)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("close ro: %v", err)
	}

	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-only Close rewrote the compressed file")
	}
}

func TestPackUnpack(t *testing.T) {
	dir := t.TempDir()
	rawA := filepath.Join(dir, "a.db")
	packed := filepath.Join(dir, "a.db.az")
	rawC := filepath.Join(dir, "c.db")

	db, err := sqlite.Open(sqlite.Config{Path: rawA})
	if err != nil {
		t.Fatalf("open a: %v", err)
	}
	mustExec(t, db, `CREATE TABLE t (v TEXT)`)
	mustExec(t, db, `INSERT INTO t VALUES ('roundtrip')`)
	if err := db.Close(); err != nil {
		t.Fatalf("close a: %v", err)
	}

	if err := Pack(packed, rawA, CompressionBetter); err != nil {
		t.Fatalf("pack: %v", err)
	}
	if !isCompressed(t, packed) {
		t.Fatal("Pack output is not compressed")
	}
	if err := Unpack(rawC, packed); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if isCompressed(t, rawC) {
		t.Fatal("Unpack output is not a raw SQLite database")
	}

	db2, err := sqlite.Open(sqlite.Config{Path: rawC})
	if err != nil {
		t.Fatalf("open c: %v", err)
	}
	defer db2.Close()
	var v string
	if err := db2.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil || v != "roundtrip" {
		t.Fatalf("got (%q, %v), want (roundtrip, nil)", v, err)
	}
}

func TestRejectInMemoryAndVFS(t *testing.T) {
	cases := []sqlite.Config{
		{Path: sqlite.InMemory},
		{Path: "x.db", Mode: sqlite.ModeMemory},
		{Path: "x.db", VFS: "somevfs"},
		{Path: ""},
	}
	for i, cfg := range cases {
		if _, err := Open(cfg, Options{}); err == nil {
			t.Errorf("case %d: Open(%+v) succeeded, want error", i, cfg)
		}
	}
}

func TestOpenFailureLeavesDestUntouched(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "missing.db.az")
	// Read-only open of a path that does not exist must fail — and must not
	// create or write the destination (the unarmed recompressor stays inert).
	if _, err := Open(sqlite.Config{Path: dest, Mode: sqlite.ModeReadOnly}, Options{}); err == nil {
		t.Fatal("read-only open of a missing file succeeded, want error")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("destination was created by a failed open (stat err = %v)", err)
	}
}

func TestUnknownFormatRejected(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(dest, []byte("not sqlite, not an az frame, just junk bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(sqlite.Config{Path: dest}, Options{}); err == nil {
		t.Fatal("Open of an unknown-format file succeeded, want error")
	}
}

func TestWALPersistence(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "wal.db.az")
	db, err := Open(sqlite.Config{Path: dest, Pragmas: sqlite.RecommendedPragmas()}, Options{})
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	mustExec(t, db, `CREATE TABLE t (v INTEGER)`)
	for i := range 500 { // leave uncheckpointed frames in the WAL
		mustExec(t, db, `INSERT INTO t VALUES (?)`, i)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := Open(sqlite.Config{Path: dest}, Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	var n int
	if err := db2.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 500 {
		t.Fatalf("got %d rows, want 500 — WAL frames not consolidated before compression", n)
	}
}

func TestTinyJunkRejectedNoClobber(t *testing.T) {
	for _, content := range []string{"X", "XY", "XYZ"} {
		dest := filepath.Join(t.TempDir(), "tiny.db")
		if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(sqlite.Config{Path: dest}, Options{}); err == nil {
			t.Fatalf("Open of %d-byte junk succeeded, want error", len(content))
		}
		got, err := os.ReadFile(dest) // must be untouched (no clobber)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != content {
			t.Fatalf("original file clobbered: got %q, want %q", got, content)
		}
	}
}

func TestMissingDestDirRejected(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "nope", "app.db.az") // parent "nope" does not exist
	if _, err := Open(sqlite.Config{Path: dest}, Options{}); err == nil {
		t.Fatal("Open with a missing destination directory succeeded, want error")
	}
}

func TestDoubleCloseIdempotent(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "dc.db.az")
	db, err := Open(sqlite.Config{Path: dest}, Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustExec(t, db, `CREATE TABLE t (v INTEGER)`)
	if err := db.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	first, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	second, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("second Close changed the on-disk file")
	}
}

func TestEmptyFileTreatedAsFresh(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "empty.db.az")
	if err := os.WriteFile(dest, nil, 0o600); err != nil { // 0-byte file
		t.Fatal(err)
	}
	db, err := Open(sqlite.Config{Path: dest}, Options{})
	if err != nil {
		t.Fatalf("open empty: %v", err)
	}
	mustExec(t, db, `CREATE TABLE t (v INTEGER)`)
	mustExec(t, db, `INSERT INTO t VALUES (1)`)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !isCompressed(t, dest) {
		t.Fatal("fresh DB from an empty file was not compressed on close")
	}
	db2, err := Open(sqlite.Config{Path: dest}, Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	var v int
	if err := db2.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("got (%d, %v), want (1, nil)", v, err)
	}
}
