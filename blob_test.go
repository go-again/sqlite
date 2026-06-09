package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
)

// TestBLOB_RoundTrip inserts a binary BLOB containing the full 256-byte
// universe (every possible byte value, including embedded NULs) and reads
// it back via []byte. This catches:
//   - bind-blob length truncation at NUL
//   - column-blob length-vs-zero-terminated mismatches
//   - encoding/decoding any byte that happens to look like a metacharacter
//     in one of the time/text paths
func TestBLOB_RoundTrip(t *testing.T) {
	db, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, b BLOB)"); err != nil {
		t.Fatal(err)
	}

	// A 256-byte BLOB containing 0x00..0xFF in order — exercises the full
	// byte alphabet, including NUL bytes which would truncate a C string.
	want := make([]byte, 256)
	for i := range want {
		want[i] = byte(i)
	}
	if _, err := db.Exec("INSERT INTO t (id, b) VALUES (1, ?)", want); err != nil {
		t.Fatal(err)
	}

	var got []byte
	if err := db.QueryRow("SELECT b FROM t WHERE id = 1").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("BLOB round-trip mismatch:\n want %d bytes\n got  %d bytes", len(want), len(got))
		// Find the first divergence for diagnostic clarity.
		for i := 0; i < len(want) && i < len(got); i++ {
			if got[i] != want[i] {
				t.Errorf("first divergence at byte %d: want=0x%02x got=0x%02x", i, want[i], got[i])
				break
			}
		}
	}
}

// TestBLOB_Zeroblob asserts that SQLite's zeroblob(N) function — the
// canonical way to reserve fixed-length blob space without copying actual
// bytes through the bind path — round-trips as N zero bytes.
//
// This matters for callers using sqlite as a binary store and pre-
// allocating slots they fill via the (CGo-style) sqlite3_blob_open API,
// which our fork doesn't surface yet but the zeroblob value still has to
// scan correctly through database/sql.
func TestBLOB_Zeroblob(t *testing.T) {
	db, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, b BLOB)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t (id, b) VALUES (1, zeroblob(64))"); err != nil {
		t.Fatal(err)
	}

	var got []byte
	if err := db.QueryRow("SELECT b FROM t WHERE id = 1").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 64 {
		t.Fatalf("zeroblob length=%d, want 64", len(got))
	}
	for i, b := range got {
		if b != 0 {
			t.Errorf("zeroblob byte %d = 0x%02x, want 0x00", i, b)
			break
		}
	}
}

// TestBLOB_EmptyBLOB asserts an explicitly empty []byte round-trips as a
// zero-length slice. Note: this driver collapses empty and nil BLOBs to
// the same scan result (a nil []byte with len()==0) because the column-blob
// path returns nil on bytes==0 — a real semantic loss vs SQL's distinction
// between an empty BLOB and SQLITE_NULL, but consistent with how modernc
// surfaces zero-length BLOBs and what most callers actually want
// (`len(b) == 0` covers both).
//
// Asserting the relaxed contract here so a future patch that surfaces empty
// BLOBs as non-nil slices is recognized as a behavior change rather than a
// bug.
func TestBLOB_EmptyBLOB(t *testing.T) {
	db, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, b BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t (id, b) VALUES (1, ?)", []byte{}); err != nil {
		t.Fatal(err)
	}

	var got []byte
	if err := db.QueryRow("SELECT b FROM t WHERE id = 1").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty-BLOB round-trip got %v (len=%d), want zero-length", got, len(got))
	}
}

// TestBLOB_ReentrantOpenFromUDF pins the fix for a stack-move bug in
// OpenBlob. The output blob-handle slot for sqlite3_blob_open used to be the
// address of a Go stack variable passed as a uintptr; the runtime does not
// track such a pointer, so when OpenBlob ran reentrantly from inside a UDF —
// deep in the sqlite3_step call graph, where a Go stack move is likely — the
// C write could land on stale memory, leaving the handle 0 and Size() == 0.
// A WriteAt would then fail "write past end of blob". The modernc bump that
// shipped SQLite 3.53.2 changed the reentrant call depth enough to surface
// it (first seen via ext/blobio); this pins it at the root where OpenBlob
// lives. The fix allocates the slot in C memory via conn.malloc.
func TestBLOB_ReentrantOpenFromUDF(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()

	// blobsize opens a write handle on the host conn and returns its size —
	// the reentrant path: OpenBlob is called while the SELECT that invokes
	// the UDF is being stepped.
	if err := c.RegisterFunc("blobsize", func(rowid int64) (int64, error) {
		b, err := c.OpenBlob("main", "blobs", "b", rowid, true)
		if err != nil {
			return 0, err
		}
		defer func() { _ = b.Close() }()
		return b.Size(), nil
	}, false); err != nil {
		t.Fatalf("RegisterFunc blobsize: %v", err)
	}

	if _, err := sc.ExecContext(ctx, `CREATE TABLE blobs(b BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO blobs(b) VALUES (zeroblob(?))`, 16); err != nil {
		t.Fatal(err)
	}

	var size int64
	if err := sc.QueryRowContext(ctx, `SELECT blobsize(1)`).Scan(&size); err != nil {
		t.Fatalf("blobsize: %v", err)
	}
	if size != 16 {
		t.Errorf("reentrant OpenBlob Size() = %d, want 16 (stack-move regression)", size)
	}
}
