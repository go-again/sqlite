package vault

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	sqlite "gosqlite.org"
)

// memAnchor is an in-memory monotonic ReplayAnchor for tests — it stands in for a
// TPM/keystore counter (the external, un-rollback-able store).
type memAnchor struct {
	mu  sync.Mutex
	gen uint64
}

func (m *memAnchor) LoadGeneration() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen, nil
}

func (m *memAnchor) StoreGeneration(g uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g > m.gen {
		m.gen = g
	}
	return nil
}

// TestAnchorRejectsRollback is the core anti-replay scenario: a complete, validly
// signed EARLIER image is restored over a database that has since advanced, and the
// anchor (which advanced past it) rejects the stale image.
func TestAnchorRejectsRollback(t *testing.T) {
	key := randKey(t)
	anchor := &memAnchor{}
	path := filepath.Join(t.TempDir(), "anchored.db")
	opts := func() Options { return Options{Key: key, Authenticate: true, Anchor: anchor} }

	// Session 1: create + write; the anchor records this generation.
	db, err := Open(sqlite.Config{Path: path}, opts())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		if _, err := db.Exec(`INSERT INTO t VALUES(?)`, "old"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	floorAfterS1 := anchor.gen
	if floorAfterS1 == 0 {
		t.Fatal("anchor did not advance after the first session")
	}
	snap, err := os.ReadFile(path) // a complete, validly-signed state at generation floorAfterS1
	if err != nil {
		t.Fatal(err)
	}

	// Session 2: advance the database (and the anchor) past the snapshot.
	db2, err := Open(sqlite.Config{Path: path}, opts())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		if _, err := db2.Exec(`INSERT INTO t VALUES(?)`, "new"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db2.Close(); err != nil {
		t.Fatal(err)
	}
	if anchor.gen <= floorAfterS1 {
		t.Fatalf("anchor did not advance in session 2 (%d <= %d)", anchor.gen, floorAfterS1)
	}

	// Roll the file back to the session-1 image and reopen: the anchor floor is now
	// above it, so the stale (but validly signed) state must be rejected.
	if err := os.WriteFile(path, snap, 0o600); err != nil {
		t.Fatal(err)
	}
	rdb, err := Open(sqlite.Config{Path: path}, opts())
	if err == nil {
		var n int
		if qerr := rdb.QueryRow(`SELECT count(*) FROM t`).Scan(&n); qerr == nil {
			t.Errorf("rolled-back database opened and read %d rows; want rejection", n)
		}
		_ = rdb.Close()
	}
}

// TestAnchorAdvancesAndReopens: a database that only moves forward reopens cleanly
// (committed generation stays at or above the floor).
func TestAnchorAdvancesAndReopens(t *testing.T) {
	key := randKey(t)
	anchor := &memAnchor{}
	path := filepath.Join(t.TempDir(), "fwd.db")
	opts := Options{Key: key, Authenticate: true, Anchor: anchor}

	db, err := Open(sqlite.Config{Path: path}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v); INSERT INTO t VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	db2, err := Open(sqlite.Config{Path: path}, opts)
	if err != nil {
		t.Fatalf("forward reopen rejected: %v", err)
	}
	defer db2.Close()
	var n int
	if err := db2.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("count = (%d, %v), want 1", n, err)
	}
}

// TestAnchorRejectsTruncateToEmpty: replacing the database with an empty file is a
// rollback to before any commit, and the anchor catches it.
func TestAnchorRejectsTruncateToEmpty(t *testing.T) {
	key := randKey(t)
	anchor := &memAnchor{}
	path := filepath.Join(t.TempDir(), "trunc.db")
	opts := Options{Key: key, Authenticate: true, Anchor: anchor}

	db, err := Open(sqlite.Config{Path: path}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if err := os.WriteFile(path, nil, 0o600); err != nil { // truncate to empty
		t.Fatal(err)
	}
	if rdb, err := Open(sqlite.Config{Path: path}, opts); err == nil {
		var n int
		if qerr := rdb.QueryRow(`SELECT count(*) FROM t`).Scan(&n); qerr == nil {
			t.Error("truncated-to-empty database opened and read; want rejection")
		}
		_ = rdb.Close()
	}
}

// TestAnchorRequiresAuth: an anchor without authenticated mode is meaningless (the
// generation would be forgeable) and is rejected at open.
func TestAnchorRequiresAuth(t *testing.T) {
	key := randKey(t)
	path := filepath.Join(t.TempDir(), "noauth.db")
	if _, err := Open(sqlite.Config{Path: path}, Options{Key: key, Anchor: &memAnchor{}}); err == nil {
		t.Fatal("Anchor without authenticated mode: want an error")
	}
}

// TestFileAnchor exercises the reference file-backed anchor: it round-trips and is
// monotonic.
func TestFileAnchor(t *testing.T) {
	a := FileAnchor(filepath.Join(t.TempDir(), "floor"))
	if g, err := a.LoadGeneration(); err != nil || g != 0 {
		t.Fatalf("fresh anchor = (%d, %v), want 0", g, err)
	}
	if err := a.StoreGeneration(5); err != nil {
		t.Fatal(err)
	}
	if g, _ := a.LoadGeneration(); g != 5 {
		t.Fatalf("after store(5) = %d, want 5", g)
	}
	if err := a.StoreGeneration(3); err != nil { // lower → ignored
		t.Fatal(err)
	}
	if g, _ := a.LoadGeneration(); g != 5 {
		t.Fatalf("after store(3) = %d, want 5 (monotonic)", g)
	}
}
