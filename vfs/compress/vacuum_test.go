package compress

// Increment-4 VACUUM and churn tests: VACUUM rewrites every page, the hardest
// workout for the block allocator (mass allocation + supersession + reuse).
// These run a real database through Open.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	sqlite "gosqlite.org"
)

// batchInsert inserts n compressible rows in a single transaction (one commit),
// which keeps these tests fast despite per-transaction fsync durability.
func batchInsert(t *testing.T, db *sqlite.DB, n int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO t (v) VALUES (?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for i := range n {
		if _, err := stmt.Exec(strings.Repeat(fmt.Sprintf("payload-%d-", i%50), 10)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestLiveVacuum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vac.dbz")

	db := openLive(t, path, Options{})
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	batchInsert(t, db, 2000)
	if _, err := db.Exec(`DELETE FROM t WHERE k % 10 != 0`); err != nil { // keep 200 rows
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		t.Fatalf("vacuum: %v", err)
	}

	assertOKCount := func(want int) {
		var ic string
		if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
			t.Fatalf("integrity_check = (%q, %v), want ok", ic, err)
		}
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n != want {
			t.Fatalf("count = (%d, %v), want %d", n, err, want)
		}
	}
	assertOKCount(200)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen: the vacuumed database is intact on disk.
	db = openLive(t, path, Options{})
	defer db.Close()
	assertOKCount(200)
}

func TestLiveChurnDoesNotGrowUnbounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "churn.dbz")

	db := openLive(t, path, Options{})
	if _, err := db.Exec(`CREATE TABLE t (k INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	const cycles = 5
	sizes := make([]int64, cycles)
	for c := range cycles {
		batchInsert(t, db, 500)
		if _, err := db.Exec(`DELETE FROM t`); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := db.Exec(`VACUUM`); err != nil {
			t.Fatalf("vacuum cycle %d: %v", c, err)
		}
		sizes[c] = fileSize(t, path)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	t.Logf("per-cycle at-rest sizes: %v", sizes)

	// Block reuse means the container plateaus instead of growing ~linearly with
	// the number of insert/delete/VACUUM cycles. A broken free list would grow
	// the file every cycle.
	if sizes[cycles-1] > sizes[1]*2 {
		t.Fatalf("at-rest file grew unbounded under churn: %v (block reuse broken?)", sizes)
	}
}
