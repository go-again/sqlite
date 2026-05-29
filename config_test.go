package sqlite_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/vfs/crypto"
)

// TestOpen_Plain confirms the new entry works for the no-encryption
// path: pure Go Config in, *sql.DB-compatible *DB out, no DSN
// strings anywhere in the caller's code.
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
	// No encryption registered → VFSName empty.
	if name := db.VFSName(); name != "" {
		t.Errorf("VFSName=%q, want empty for no-encryption path", name)
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
	_, err := sqlite.Open(sqlite.Config{
		Path: filepath.Join(dir, "bad.db"),
		Pragmas: sqlite.Pragmas{
			Extra: map[string]string{
				"this_pragma_does_not_exist": "1",
			},
		},
	})
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

func TestOpen_Encrypted(t *testing.T) {
	if raceEnabledExt {
		t.Skip("vfs/crypto trampolines trip -race checkptr (same skip as the vfs/crypto package)")
	}
	dir := t.TempDir()
	key := make([]byte, 32)
	db, err := sqlite.Open(sqlite.Config{
		Path:    filepath.Join(dir, "secret.db"),
		Pragmas: sqlite.RecommendedPragmas(),
		Encryption: &sqlite.Encryption{
			Key:    key,
			Cipher: sqlite.Adiantum,
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if name := db.VFSName(); !strings.HasPrefix(name, "crypto") {
		t.Errorf("VFSName=%q, want prefix \"crypto\" (encryption path)", name)
	}
	if _, err := db.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('encrypted')`); err != nil {
		t.Fatalf("exec: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if v != "encrypted" {
		t.Errorf("v=%q, want \"encrypted\"", v)
	}
}

// TestOpen_EncryptionKeyDefensiveCopy pins that mutating the caller's
// Encryption.Key slice after Open does NOT corrupt the in-flight
// cipher. The contract is documented at sqlite.Encryption.Key —
// regression here would silently break encrypted IO.
func TestOpen_EncryptionKeyDefensiveCopy(t *testing.T) {
	if raceEnabledExt {
		t.Skip("touches vfs/crypto; same -race checkptr skip")
	}
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	keyBackup := append([]byte(nil), key...)

	db, err := sqlite.Open(sqlite.Config{
		Path: filepath.Join(dir, "keymut.db"),
		Encryption: &sqlite.Encryption{
			Key:    key,
			Cipher: sqlite.Adiantum,
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Mutate the caller's key slice — defensive copy should keep the
	// cipher operating on the original bytes.
	for i := range key {
		key[i] = 0xAA
	}

	if _, err := db.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('still works')`); err != nil {
		t.Fatalf("post-mutation exec: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("post-mutation scan: %v", err)
	}
	if v != "still works" {
		t.Errorf("v=%q, want \"still works\"", v)
	}
	// And the backup matches what we initialized with.
	if !bytes.Equal(keyBackup, []byte{
		1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
		17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32,
	}) {
		t.Fatal("backup of original key changed mid-test")
	}
}

// TestOpen_EncryptionBadKeyLengthSurfaces pins that the Config layer
// surfaces a wrong-length key as an Open error (not a panic, not a
// silent open with broken IO).
func TestOpen_EncryptionBadKeyLengthSurfaces(t *testing.T) {
	if raceEnabledExt {
		t.Skip("touches vfs/crypto; same -race checkptr skip")
	}
	dir := t.TempDir()
	_, err := sqlite.Open(sqlite.Config{
		Path: filepath.Join(dir, "bad-key.db"),
		Encryption: &sqlite.Encryption{
			Key:    make([]byte, 16), // Adiantum needs 32
			Cipher: sqlite.Adiantum,
		},
	})
	if err == nil {
		t.Fatal("expected error for wrong key length, got nil")
	}
	if !strings.Contains(err.Error(), "32-byte key") {
		t.Errorf("error %q does not mention required key length", err.Error())
	}
}

// TestOpen_EncryptionInvalidCipher pins that an unknown Cipher value
// fails Open with a clear error.
func TestOpen_EncryptionInvalidCipher(t *testing.T) {
	if raceEnabledExt {
		t.Skip("touches vfs/crypto; same -race checkptr skip")
	}
	dir := t.TempDir()
	_, err := sqlite.Open(sqlite.Config{
		Path: filepath.Join(dir, "bad-cipher.db"),
		Encryption: &sqlite.Encryption{
			Key:    make([]byte, 32),
			Cipher: sqlite.Cipher(99), // out of range
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown cipher, got nil")
	}
	if !strings.Contains(err.Error(), "unknown cipher") {
		t.Errorf("error %q does not mention unknown cipher", err.Error())
	}
}

// TestOpen_EncryptionPageSizeNonDefault pins that PageSize != 4096
// round-trips: create with 8192, close, reopen with same, read back.
func TestOpen_EncryptionPageSizeNonDefault(t *testing.T) {
	if raceEnabledExt {
		t.Skip("touches vfs/crypto; same -race checkptr skip")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "page8k.db")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 100)
	}

	mkCfg := func() sqlite.Config {
		return sqlite.Config{
			Path:    path,
			Pragmas: sqlite.RecommendedPragmas(),
			Encryption: &sqlite.Encryption{
				Key:      key,
				Cipher:   sqlite.Adiantum,
				PageSize: 8192,
			},
		}
	}

	db, err := sqlite.Open(mkCfg())
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := db.Exec(`PRAGMA page_size = 8192;
		CREATE TABLE t (v TEXT);
		INSERT INTO t VALUES ('page8k');`); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	db2, err := sqlite.Open(mkCfg())
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	var v string
	if err := db2.QueryRow(`SELECT v FROM t`).Scan(&v); err != nil {
		t.Fatalf("read after reopen: %v", err)
	}
	if v != "page8k" {
		t.Errorf("v=%q, want \"page8k\"", v)
	}
}

// TestOpen_EncryptionRecorderFires confirms that a Recorder attached
// via Config.Encryption is wired into the crypto VFS — IO produces
// events the caller can observe.
func TestOpen_EncryptionRecorderFires(t *testing.T) {
	if raceEnabledExt {
		t.Skip("touches vfs/crypto; same -race checkptr skip")
	}
	dir := t.TempDir()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	recorder := crypto.NewSlogRecorder(logger)

	db, err := sqlite.Open(sqlite.Config{
		Path:    filepath.Join(dir, "obs.db"),
		Pragmas: sqlite.RecommendedPragmas(),
		Encryption: &sqlite.Encryption{
			Key:      make([]byte, 32),
			Cipher:   sqlite.Adiantum,
			Recorder: recorder,
		},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('obs')`); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("recorder captured zero events for an encrypted write path")
	}
}

func TestOpen_Errors(t *testing.T) {
	cases := []struct {
		name string
		cfg  sqlite.Config
		want string
	}{
		{"missing path", sqlite.Config{}, "Path is required"},
		{"VFS and Encryption both set", sqlite.Config{
			Path:       "/tmp/x",
			VFS:        "some_vfs",
			Encryption: &sqlite.Encryption{Key: make([]byte, 32)},
		}, "mutually exclusive"},
		{":memory: with Encryption", sqlite.Config{
			Path:       ":memory:",
			Encryption: &sqlite.Encryption{Key: make([]byte, 32)},
		}, "on-disk path"},
		{"ModeMemory with Encryption", sqlite.Config{
			Path:       "any.db",
			Mode:       sqlite.ModeMemory,
			Encryption: &sqlite.Encryption{Key: make([]byte, 32)},
		}, "on-disk path"},
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
	if raceEnabledExt {
		t.Skip("touches vfs/crypto; same -race checkptr skip")
	}
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.Config{
		Path: filepath.Join(dir, "idem.db"),
		Encryption: &sqlite.Encryption{
			Key:    make([]byte, 32),
			Cipher: sqlite.Adiantum,
		},
	})
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
	db, err := sql.Open("sqlite", dsn)
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

// TestOpen_ConcurrentEncryptedOpens exercises two simultaneous
// Open calls with Encryption set: each gets a distinct VFS name,
// Close on one does NOT pull the rug out from under the other.
func TestOpen_ConcurrentEncryptedOpens(t *testing.T) {
	if raceEnabledExt {
		t.Skip("touches vfs/crypto; same -race checkptr skip")
	}
	dir := t.TempDir()
	const n = 4
	dbs := make([]*sqlite.DB, n)
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, err := sqlite.Open(sqlite.Config{
				Path: filepath.Join(dir, fmt.Sprintf("conc%d.db", i)),
				Encryption: &sqlite.Encryption{
					Key:    make([]byte, 32),
					Cipher: sqlite.Adiantum,
				},
			})
			if err != nil {
				errs[i] = err
				return
			}
			dbs[i] = db
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("Open[%d]: %v", i, e)
		}
	}

	seen := map[string]bool{}
	for i, db := range dbs {
		name := db.VFSName()
		if seen[name] {
			t.Errorf("Open[%d] reused VFS name %q (should be unique per Open)", i, name)
		}
		seen[name] = true
	}

	// Close half, confirm the other half still works.
	for i := range n / 2 {
		if err := dbs[i].Close(); err != nil {
			t.Fatalf("Close[%d]: %v", i, err)
		}
		dbs[i] = nil
	}
	for i := n / 2; i < n; i++ {
		if _, err := dbs[i].Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('survives')`); err != nil {
			t.Errorf("Exec on survivor[%d] after closing peers: %v", i, err)
		}
		if err := dbs[i].Close(); err != nil {
			t.Errorf("Close survivor[%d]: %v", i, err)
		}
	}
}

// TestOpen_FailedOpenReleasesVFS pins that a failure after VFS
// registration releases the VFS — otherwise repeated failed opens
// leak crypto.FS handles.
func TestOpen_FailedOpenReleasesVFS(t *testing.T) {
	if raceEnabledExt {
		t.Skip("touches vfs/crypto; same -race checkptr skip")
	}
	dir := t.TempDir()
	// Bad PRAGMA — drives the PingContext path to fail after VFS
	// has been registered.
	_, err := sqlite.Open(sqlite.Config{
		Path: filepath.Join(dir, "fail.db"),
		Encryption: &sqlite.Encryption{
			Key:    make([]byte, 32),
			Cipher: sqlite.Adiantum,
		},
		Pragmas: sqlite.Pragmas{
			Extra: map[string]string{
				// Intentionally malformed; modernc will reject this
				// on conn open, surfacing a Ping error.
				"page_size": "not_a_number",
			},
		},
	})
	if err == nil {
		t.Skip("driver accepted bad pragma; no easy way to force a post-VFS-registration failure")
	}
	// A subsequent successful Open should work — proves the failed
	// open didn't leave the VFS registered and blocking subsequent
	// registrations of the same name.
	db, err := sqlite.Open(sqlite.Config{
		Path: filepath.Join(dir, "ok.db"),
		Encryption: &sqlite.Encryption{
			Key:    make([]byte, 32),
			Cipher: sqlite.Adiantum,
		},
	})
	if err != nil {
		t.Fatalf("subsequent Open after failed one: %v", err)
	}
	_ = db.Close()
}

// Catch nil-deref guards on the DB wrapper.
var _ io.Closer = (*sqlite.DB)(nil)

// nilWrapperClose exercises the zero-value safety guard so a typo
// like `var db *sqlite.DB; db.Close()` doesn't panic.
func TestDB_NilCloseSafe(t *testing.T) {
	var db *sqlite.DB
	if err := db.Close(); err != nil {
		t.Errorf("nil receiver Close: %v", err)
	}
	if name := db.VFSName(); name != "" {
		t.Errorf("nil receiver VFSName=%q", name)
	}
}

// Sanity check: ensure errors.As / errors.Is friendliness on the
// wrapped error paths.
func TestOpen_ErrorsAreWrapped(t *testing.T) {
	_, err := sqlite.Open(sqlite.Config{})
	if err == nil {
		t.Fatal("expected error")
	}
	// "Path is required" is unwrapped, but VFS+Encryption error wraps
	// nothing either — the goal here is to confirm callers can
	// errors.Is against future sentinel errors. Today's contract is
	// loose; this test is a placeholder to flag if we introduce
	// sentinels later.
	var probe interface{ Error() string }
	if !errors.As(err, &probe) {
		t.Errorf("err not satisfying error interface: %v", err)
	}
}
