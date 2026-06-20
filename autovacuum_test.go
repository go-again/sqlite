package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func pragmaInt(t *testing.T, db *DB, pragma string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(&n); err != nil {
		t.Fatalf("PRAGMA %s: %v", pragma, err)
	}
	return n
}

// grow inserts rows then deletes them, leaving free pages behind.
func growThenFree(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE t(b BLOB)`); err != nil {
		t.Fatal(err)
	}
	blob := strings.Repeat("x", 4000)
	for range 500 {
		if _, err := db.ExecContext(ctx, `INSERT INTO t(b) VALUES (?)`, blob); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM t`); err != nil {
		t.Fatal(err)
	}
}

func TestConfigAutoVacuumIncrementalFresh(t *testing.T) {
	db, err := Open(Config{
		Path:    filepath.Join(t.TempDir(), "av.db"),
		Pragmas: Pragmas{JournalMode: JournalWAL, AutoVacuum: AutoVacuumIncremental},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// The mode must have taken effect at creation, even though WAL was also
	// requested (auto_vacuum is emitted first).
	if got := pragmaInt(t, db, "auto_vacuum"); got != 2 {
		t.Fatalf("auto_vacuum = %d, want 2 (incremental)", got)
	}

	growThenFree(t, db)
	if free := pragmaInt(t, db, "freelist_count"); free == 0 {
		t.Fatal("freelist_count = 0 after delete; expected free pages to reclaim")
	}
	if err := db.IncrementalVacuum(context.Background(), 0); err != nil {
		t.Fatalf("IncrementalVacuum: %v", err)
	}
	if free := pragmaInt(t, db, "freelist_count"); free != 0 {
		t.Fatalf("freelist_count = %d after IncrementalVacuum, want 0", free)
	}
}

func TestSetAutoVacuumConvertsExisting(t *testing.T) {
	db, err := Open(Config{
		Path:    filepath.Join(t.TempDir(), "conv.db"),
		Pragmas: Pragmas{JournalMode: JournalWAL}, // no auto_vacuum → NONE
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	growThenFree(t, db)
	if got := pragmaInt(t, db, "auto_vacuum"); got != 0 {
		t.Fatalf("auto_vacuum = %d, want 0 (none) before conversion", got)
	}

	if err := db.SetAutoVacuum(context.Background(), AutoVacuumIncremental); err != nil {
		t.Fatalf("SetAutoVacuum: %v", err)
	}
	if got := pragmaInt(t, db, "auto_vacuum"); got != 2 {
		t.Fatalf("auto_vacuum = %d after conversion, want 2", got)
	}
	// VACUUM already compacted; the freelist is empty and incremental_vacuum
	// is now a working no-op.
	if err := db.IncrementalVacuum(context.Background(), 0); err != nil {
		t.Fatalf("IncrementalVacuum after convert: %v", err)
	}
}

func TestSetAutoVacuumInvalid(t *testing.T) {
	db, err := Open(Config{Path: filepath.Join(t.TempDir(), "x.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, bad := range []AutoVacuumMode{"", "bogus"} {
		if err := db.SetAutoVacuum(context.Background(), bad); err == nil {
			t.Errorf("SetAutoVacuum(%q): want error, got nil", bad)
		}
	}
}

// TestAutoVacuumRenderedInDSN checks BuildDSN emits the auto_vacuum pragma.
// Application order is the driver's job — it re-sorts _pragma values — so the
// emit position is not what makes auto_vacuum take effect; connection-open
// timing before the first CREATE TABLE is (see TestConfigAutoVacuumIncrementalFresh).
func TestAutoVacuumRenderedInDSN(t *testing.T) {
	dsn := BuildDSN(Config{
		Path:    "x.db",
		Pragmas: Pragmas{JournalMode: JournalWAL, AutoVacuum: AutoVacuumIncremental},
	})
	if !strings.Contains(dsn, "auto_vacuum") {
		t.Fatalf("BuildDSN did not render auto_vacuum: %q", dsn)
	}
}
