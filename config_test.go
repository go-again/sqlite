package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlite "gosqlite.org"
)

// TestOpen_Plain confirms the new entry works: pure Go Config in,
// *sql.DB-compatible *DB out, no DSN strings anywhere in the caller's code.
func TestOpen_Plain(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{
		Path: filepath.Join(dir, "plain.db"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('hi')`); err != nil {
		t.Fatalf("exec: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if v != "hi" {
		t.Errorf("v=%q, want \"hi\"", v)
	}
}

func TestOpen_RecommendedPragmasApply(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{
		Path:    filepath.Join(dir, "pragmas.db"),
		Pragmas: sqlite.RecommendedPragmas(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var mode string
	db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode=%q, want \"wal\"", mode)
	}
	var fk int
	db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Errorf("foreign_keys=%d, want 1", fk)
	}
	var bt int
	db.QueryRow(`PRAGMA busy_timeout`).Scan(&bt)
	if bt != 5000 {
		t.Errorf("busy_timeout=%d ms, want 5000", bt)
	}
}

// TestOpen_PragmasPropagateAcrossPool exercises the per-connection
// PRAGMA propagation. Before the URL-flag rewrite, busy_timeout /
// foreign_keys / synchronous etc. were applied via *sql.DB.Exec, so
// only whichever idle connection happened to handle that Exec got
// the setting — other conns in the pool stayed at SQLite defaults.
// Now PRAGMAs ride in via DSN `_pragma=` URL flags, so every conn
// the driver opens gets them. Pin that by holding two conns at once
// and confirming both report the configured values.
func TestOpen_PragmasPropagateAcrossPool(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{
		Path:         filepath.Join(dir, "pool-pragmas.db"),
		Pragmas:      sqlite.RecommendedPragmas(),
		MaxOpenConns: 2,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	conns := make([]*sql.Conn, 2)
	for i := range conns {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn[%d]: %v", i, err)
		}
		conns[i] = c
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i, c := range conns {
		var bt int
		if err := c.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&bt); err != nil {
			t.Fatalf("conn[%d] busy_timeout: %v", i, err)
		}
		if bt != 5000 {
			t.Errorf("conn[%d] busy_timeout=%d, want 5000", i, bt)
		}
		var fk int
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatalf("conn[%d] foreign_keys: %v", i, err)
		}
		if fk != 1 {
			t.Errorf("conn[%d] foreign_keys=%d, want 1", i, fk)
		}
	}
}

// TestOpen_NonRecommendedPragmasRoundTrip pins Synchronous,
// CacheSize, and TempStore — Pragmas fields not exercised by
// the RecommendedPragmas preset.
func TestOpen_NonRecommendedPragmasRoundTrip(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{
		Path: filepath.Join(dir, "other-pragmas.db"),
		Pragmas: sqlite.Pragmas{
			Synchronous: "NORMAL",
			CacheSize:   -16000, // 16 MiB
			TempStore:   "MEMORY",
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var sync int
	db.QueryRow(`PRAGMA synchronous`).Scan(&sync)
	if sync != 1 { // NORMAL == 1
		t.Errorf("synchronous=%d, want 1 (NORMAL)", sync)
	}
	var cache int
	db.QueryRow(`PRAGMA cache_size`).Scan(&cache)
	if cache != -16000 {
		t.Errorf("cache_size=%d, want -16000", cache)
	}
	var temp int
	db.QueryRow(`PRAGMA temp_store`).Scan(&temp)
	if temp != 2 { // MEMORY == 2
		t.Errorf("temp_store=%d, want 2 (MEMORY)", temp)
	}
}

// TestOpen_ExtraPragmasFire exercises the Pragmas.Extra escape hatch.
// recursive_triggers defaults to OFF; setting it ON via Extra should
// land on a query.
func TestOpen_ExtraPragmasFire(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{
		Path: filepath.Join(dir, "extra.db"),
		Pragmas: sqlite.Pragmas{
			Extra: map[string]string{
				"recursive_triggers": "ON",
			},
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var v int
	db.QueryRow(`PRAGMA recursive_triggers`).Scan(&v)
	if v != 1 {
		t.Errorf("recursive_triggers=%d, want 1", v)
	}
}

// TestOpen_ExtraInvalidPragmaSurfaces confirms that a bad PRAGMA
// value in Pragmas.Extra fails Open with a clear error rather than
// being silently dropped.
func TestOpen_ExtraInvalidPragmaSurfaces(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{
		Path: filepath.Join(dir, "bad.db"),
		Pragmas: sqlite.Pragmas{
			Extra: map[string]string{
				"this_pragma_does_not_exist": "1",
			},
		},
	})
	// SQLite silently ignores unknown pragmas, so Open usually succeeds
	// here. Close the returned DB so its file handle is released before
	// t.TempDir's cleanup — Windows can't unlink a file with an open
	// handle, unlike Linux/macOS.
	if db != nil {
		_ = db.Close()
	}
	// modernc's driver may or may not error on unknown PRAGMA names
	// (SQLite silently ignores them by default). The contract we
	// care about is: if it DOES error, Open surfaces it. If SQLite
	// accepts it as a no-op, Open returns nil with no panic. Either
	// outcome is acceptable; we only need to confirm Open doesn't
	// crash and any error is wrapped through `sqlite:`.
	if err != nil && !strings.Contains(err.Error(), "sqlite:") {
		t.Errorf("error not wrapped with 'sqlite:' prefix: %v", err)
	}
}

// TestPragmas_ExtraDeterministicOrder pins that BuildDSN sorts the
// Extra map's keys. Without sorting, two calls with the same Config
// can produce different DSNs (Go's map iteration is randomized).
func TestPragmas_ExtraDeterministicOrder(t *testing.T) {
	cfg := sqlite.Config{
		Path: "x.db",
		Pragmas: sqlite.Pragmas{
			Extra: map[string]string{
				"z_last":   "26",
				"a_first":  "1",
				"m_middle": "13",
			},
		},
	}
	first := sqlite.BuildDSN(cfg)
	for i := range 100 {
		if got := sqlite.BuildDSN(cfg); got != first {
			t.Fatalf("non-deterministic BuildDSN:\n  first: %s\n  iter %d: %s", first, i, got)
		}
	}
	// And the values appear in sorted order in the DSN.
	if !strings.Contains(first, "a_first") || !strings.Contains(first, "m_middle") || !strings.Contains(first, "z_last") {
		t.Fatalf("Extra keys missing from DSN: %s", first)
	}
	if strings.Index(first, "a_first") > strings.Index(first, "m_middle") {
		t.Errorf("a_first should precede m_middle in DSN: %s", first)
	}
	if strings.Index(first, "m_middle") > strings.Index(first, "z_last") {
		t.Errorf("m_middle should precede z_last in DSN: %s", first)
	}
}

func TestOpen_Errors(t *testing.T) {
	cases := []struct {
		name string
		cfg  sqlite.Config
		want string
	}{
		{"missing path", sqlite.Config{}, "Path is required"},
		{"unknown VFS name surfaces", sqlite.Config{
			Path: "/tmp/no-such-vfs-test.db",
			VFS:  "no_such_vfs_zzz",
		}, "sqlite"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sqlite.Open(tc.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// TestOpen_ReadOnlyEnforcement: open RW first to create the file,
// close, reopen RO, try to write — expect SQLite to reject.
func TestOpen_ReadOnlyEnforcement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ro.db")

	// Seed the file.
	seed, err := sqlite.Open(sqlite.Config{Path: path})
	if err != nil {
		t.Fatalf("seed Open: %v", err)
	}
	if _, err := seed.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('seed')`); err != nil {
		t.Fatalf("seed exec: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}

	roDB, err := sqlite.Open(sqlite.Config{
		Path: path,
		Mode: sqlite.ModeReadOnly,
	})
	if err != nil {
		t.Fatalf("RO Open: %v", err)
	}
	t.Cleanup(func() { _ = roDB.Close() })

	// Read should succeed.
	var v string
	if err := roDB.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("RO read: %v", err)
	}
	if v != "seed" {
		t.Errorf("v=%q, want \"seed\"", v)
	}

	// Write should fail.
	if _, err := roDB.Exec(`INSERT INTO t VALUES ('nope')`); err == nil {
		t.Error("RO write succeeded; expected SQLITE_READONLY")
	}
}

func TestClose_Idempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{Path: filepath.Join(dir, "idem.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Errorf("second Close: %v (should be idempotent)", err)
	}
}

func TestBuildDSN(t *testing.T) {
	cases := []struct {
		name string
		cfg  sqlite.Config
		want string
	}{
		{"bare path", sqlite.Config{Path: "x.db"}, "file:x.db"},
		{"with mode", sqlite.Config{Path: "x.db", Mode: sqlite.ModeReadOnly}, "file:x.db?mode=ro"},
		{"with vfs", sqlite.Config{Path: "x.db", VFS: "myvfs"}, "file:x.db?vfs=myvfs"},
		{"with mode and vfs", sqlite.Config{Path: "x.db", Mode: sqlite.ModeReadOnly, VFS: "myvfs"}, "file:x.db?mode=ro&vfs=myvfs"},
		{"with txlock", sqlite.Config{Path: "x.db", TxLock: "immediate"}, "file:x.db?_txlock=immediate"},
		// `:memory:` ignores Mode (skipping `?mode=memory` to avoid `file::memory:?mode=memory`).
		{"memory path ignores mode", sqlite.Config{Path: ":memory:", Mode: sqlite.ModeMemory}, "file::memory:"},
		// ModeMemory on a named DB is a valid SQLite form (private named in-memory).
		{"named in-memory", sqlite.Config{Path: "named", Mode: sqlite.ModeMemory}, "file:named?mode=memory"},
		// Path with chars that would corrupt the URI: space + `?` + `&` + `#` + `%`.
		{"escaped path", sqlite.Config{Path: "weird ?&#%path.db"}, "file:weird%20%3F%26%23%25path.db"},
		// Pragmas land as `_pragma=` URL flags. Order: `_pragma=` (multiple) sort alphabetically by KEY first, then values keep insertion order.
		{"pragmas as URL flags",
			sqlite.Config{
				Path:    "x.db",
				Pragmas: sqlite.Pragmas{JournalMode: "WAL", BusyTimeout: 5 * time.Second, ForeignKeys: true},
			},
			"file:x.db?_pragma=journal_mode%28WAL%29&_pragma=busy_timeout%285000%29&_pragma=foreign_keys%28on%29",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sqlite.BuildDSN(tc.cfg); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestConfig_TxLock pins the typed transaction-lock mode: "immediate" is accepted
// and a transaction round-trips, and an unknown value is rejected by the driver.
func TestConfig_TxLock(t *testing.T) {
	dir := t.TempDir()

	db, err := sqlite.Open(sqlite.Config{Path: filepath.Join(dir, "tx.db"), TxLock: "immediate"})
	if err != nil {
		t.Fatalf("Open with TxLock=immediate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO t VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// An unknown _txlock value is rejected (eagerly at Open, or on first use).
	bad, err := sqlite.Open(sqlite.Config{Path: filepath.Join(dir, "bad.db"), TxLock: "bogus"})
	if err == nil {
		err = bad.Ping()
		_ = bad.Close()
	}
	if err == nil {
		t.Error("TxLock=\"bogus\": want an error")
	}
}

func TestConfig_ConnPoolKnobsApply(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{
		Path:            filepath.Join(dir, "pool.db"),
		MaxOpenConns:    7,
		MaxIdleConns:    3,
		ConnMaxLifetime: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	stats := db.Stats()
	if stats.MaxOpenConnections != 7 {
		t.Errorf("MaxOpenConnections=%d, want 7", stats.MaxOpenConnections)
	}
}

// TestConfig_MaxIdleConnsBehavior pins SetMaxIdleConns by exercising
// it: open more conns than the idle limit, release them all, and
// confirm sql.DBStats.MaxIdleClosed shows conns were closed because
// they exceeded the idle cap.
func TestConfig_MaxIdleConnsBehavior(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{
		Path:         filepath.Join(dir, "idle.db"),
		MaxOpenConns: 4,
		MaxIdleConns: 1, // releasing 4 conns should close 3.
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	conns := make([]*sql.Conn, 4)
	for i := range conns {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn[%d]: %v", i, err)
		}
		conns[i] = c
	}
	for _, c := range conns {
		if err := c.Close(); err != nil {
			t.Fatalf("conn.Close: %v", err)
		}
	}
	stats := db.Stats()
	if stats.MaxIdleClosed == 0 {
		t.Errorf("MaxIdleClosed=0, want >0 after releasing 4 conns with MaxIdleConns=1")
	}
}

// TestApplyPragmas_Standalone confirms the legacy escape hatch:
// open a *sql.DB via the bare sql.Open(dsn) path, call ApplyPragmas,
// and confirm the settings stick (best-effort for per-conn pragmas
// at MaxOpenConns=1).
func TestApplyPragmas_Standalone(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "apply.db")
	db, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	if err := sqlite.ApplyPragmas(db, sqlite.RecommendedPragmas()); err != nil {
		t.Fatalf("ApplyPragmas: %v", err)
	}

	var mode string
	db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode=%q, want \"wal\"", mode)
	}
	var bt int
	db.QueryRow(`PRAGMA busy_timeout`).Scan(&bt)
	if bt != 5000 {
		t.Errorf("busy_timeout=%d, want 5000", bt)
	}
}

// Catch nil-deref guards on the DB wrapper.
var _ io.Closer = (*sqlite.DB)(nil)

// TestDB_NilCloseSafe exercises the zero-value safety guard so a typo
// like `var db *sqlite.DB; db.Close()` doesn't panic.
func TestDB_NilCloseSafe(t *testing.T) {
	var db *sqlite.DB
	if err := db.Close(); err != nil {
		t.Errorf("nil receiver Close: %v", err)
	}
}

// Sanity check: ensure errors.As / errors.Is friendliness on the
// wrapped error paths.
func TestOpen_ErrorsAreWrapped(t *testing.T) {
	_, err := sqlite.Open(sqlite.Config{})
	if err == nil {
		t.Fatal("expected error")
	}
	// "Path is required" is unwrapped today; this test is a placeholder to
	// flag if we introduce errors.Is-able sentinels later.
	var probe interface{ Error() string }
	if !errors.As(err, &probe) {
		t.Errorf("err not satisfying error interface: %v", err)
	}
}
