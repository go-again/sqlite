package crypto_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/fts"
	"github.com/go-again/sqlite/vec"
	"github.com/go-again/sqlite/vfs/crypto"

	// Auto-register sqlite-vec on every connection.
	_ "github.com/go-again/sqlite/vec"
)

// openEncryptedDB is a small helper for the integration tests: spin
// up a fresh crypto VFS with key=k, open a DB on tmpfile, return
// (db, cleanup). The cleanup closes the DB and unregisters the VFS.
func openEncryptedDB(t *testing.T, dir string, k byte) *sql.DB {
	t.Helper()
	key := freshKey(k)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	dbPath := filepath.Join(dir, fmt.Sprintf("encrypted-%d.db", k))
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestVec_KNN_InsideEncryptedDB confirms a vec0 virtual table works
// transparently inside an encrypted database. Create + insert + KNN
// + verify ranking — same flow as vec/table_test.go, just running
// through the wrapping VFS.
func TestVec_KNN_InsideEncryptedDB(t *testing.T) {
	db := openEncryptedDB(t, t.TempDir(), 10)
	db.SetMaxOpenConns(1) // virtual tables are per-conn

	ctx := context.Background()
	tbl, err := vec.Create(ctx, db, "items_vec", 4, vec.Options{
		Metric: vec.Cosine, Encoding: vec.Binary,
	})
	if err != nil {
		t.Fatalf("vec.Create: %v", err)
	}
	if err := tbl.BatchInsert(ctx, []vec.Row{
		{Rowid: 1, Embedding: []float32{1.0, 0.0, 0.0, 0.0}},
		{Rowid: 2, Embedding: []float32{0.0, 1.0, 0.0, 0.0}},
		{Rowid: 3, Embedding: []float32{0.0, 0.0, 1.0, 0.0}},
	}); err != nil {
		t.Fatalf("BatchInsert: %v", err)
	}

	hits, err := tbl.KNNSlice(ctx, []float32{1.0, 0.0, 0.0, 0.0}, 3)
	if err != nil {
		t.Fatalf("KNNSlice: %v", err)
	}
	if len(hits) == 0 || hits[0].Rowid != 1 {
		t.Errorf("top hit rowid=%d, want 1 (got hits=%+v)", hits[0].Rowid, hits)
	}
}

// TestFTS_Search_InsideEncryptedDB confirms an FTS5 index works
// transparently inside an encrypted database.
func TestFTS_Search_InsideEncryptedDB(t *testing.T) {
	db := openEncryptedDB(t, t.TempDir(), 11)
	ctx := context.Background()
	idx, err := fts.New[int64, string](ctx, db, "docs", fts.Options{
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
	})
	if err != nil {
		t.Fatalf("fts.New: %v", err)
	}
	if err := idx.Insert(ctx,
		fts.Attr[int64, string]{Key: 1, Value: "the quick brown fox"},
		fts.Attr[int64, string]{Key: 2, Value: "the lazy dog"},
		fts.Attr[int64, string]{Key: 3, Value: "another fox jumps"},
	); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	hits, err := idx.SearchSlice(ctx, fts.Term("fox"))
	if err != nil {
		t.Fatalf("SearchSlice: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("fox matches=%d, want 2 (got %+v)", len(hits), hits)
	}
}

// TestEncryptedDB_HoldsBothVecAndFTS sanity-checks that both
// subsystems can coexist in the same encrypted DB. If a future
// change broke the encryption transparency for one but not the
// other (e.g. by intercepting a specific opcode), this would catch
// it.
func TestEncryptedDB_HoldsBothVecAndFTS(t *testing.T) {
	db := openEncryptedDB(t, t.TempDir(), 12)
	db.SetMaxOpenConns(1)
	ctx := context.Background()

	tbl, err := vec.Create(ctx, db, "embeds", 3, vec.Options{
		Metric: vec.L2, Encoding: vec.JSON,
	})
	if err != nil {
		t.Fatalf("vec.Create: %v", err)
	}
	if err := tbl.Insert(ctx, 1, []float32{1, 0, 0}); err != nil {
		t.Fatalf("vec.Insert: %v", err)
	}

	idx, err := fts.New[int64, string](ctx, db, "text", fts.Options{
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
	})
	if err != nil {
		t.Fatalf("fts.New: %v", err)
	}
	if err := idx.Insert(ctx, fts.Attr[int64, string]{Key: 1, Value: "hello world"}); err != nil {
		t.Fatalf("fts.Insert: %v", err)
	}

	hits, err := tbl.KNNSlice(ctx, []float32{1, 0, 0}, 1)
	if err != nil {
		t.Fatalf("KNNSlice: %v", err)
	}
	if len(hits) != 1 || hits[0].Rowid != 1 {
		t.Errorf("vec query: %+v, want one hit rowid=1", hits)
	}
	ftsHits, err := idx.SearchSlice(ctx, fts.Term("hello"))
	if err != nil {
		t.Fatalf("SearchSlice: %v", err)
	}
	if len(ftsHits) != 1 || ftsHits[0].Key != 1 {
		t.Errorf("fts query: %+v, want one hit key=1", ftsHits)
	}
}

// TestVecAndFTS_DiskIsCiphertext confirms vec / fts data, like
// regular row data, ends up encrypted on disk. The signature being
// looked for: vec stores embeddings as raw binary, FTS5 stores
// tokenized text; neither should appear plaintext.
func TestVecAndFTS_DiskIsCiphertext(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "encrypted-13.db")
	key := freshKey(13)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	dsn := fmt.Sprintf("file:%s?vfs=%s", dbPath, name)
	db, _ := sql.Open(sqlite.DriverName, dsn)
	db.SetMaxOpenConns(1)
	ctx := context.Background()

	tbl, err := vec.Create(ctx, db, "embeds", 4, vec.Options{Metric: vec.L2, Encoding: vec.Binary})
	if err != nil {
		t.Fatalf("vec.Create: %v", err)
	}
	if err := tbl.Insert(ctx, 1, []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("vec.Insert: %v", err)
	}
	idx, err := fts.New[int64, string](ctx, db, "txt", fts.Options{
		Tokenizer: fts.Porter{Base: fts.Unicode61{}},
	})
	if err != nil {
		t.Fatalf("fts.New: %v", err)
	}
	if err := idx.Insert(ctx, fts.Attr[int64, string]{Key: 1, Value: "ftsplaintextmarker"}); err != nil {
		t.Fatalf("fts.Insert: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("fs.Close: %v", err)
	}

	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	for _, marker := range [][]byte{
		[]byte("SQLite format 3"),
		[]byte("ftsplaintextmarker"),
	} {
		if containsSubslice(raw, marker) {
			t.Errorf("on-disk file contains plaintext marker %q", marker)
		}
	}
}

func containsSubslice(haystack, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j, b := range needle {
			if haystack[i+j] != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestCrypto_XOpen_InvalidPathRejected exercises the xOpen failure
// path on the crypto VFS: the underlying default VFS rejects a path
// in a non-existent parent directory, and crypto must forward the
// failure without leaking fileMap entries. Repeated failed opens
// must not starve subsequent good opens.
func TestCrypto_XOpen_InvalidPathRejected(t *testing.T) {
	key := freshKey(99)
	name, fs, err := crypto.New(crypto.Options{Key: key})
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })

	bad := filepath.Join(t.TempDir(), "nonexistent-subdir", "x.db")
	for range 50 {
		db, err := sql.Open(sqlite.DriverName, bad+"?vfs="+name)
		if err != nil {
			db.Close()
			continue
		}
		if _, err := db.Exec(`CREATE TABLE t(v)`); err == nil {
			t.Error("expected xOpen failure on invalid path, got success")
		}
		db.Close()
	}

	good := filepath.Join(t.TempDir(), "good.db")
	db, err := sql.Open(sqlite.DriverName, good+"?vfs="+name)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(v INT)`); err != nil {
		t.Errorf("after failures, good open failed: %v", err)
	}
}
