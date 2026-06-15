package sqlite

import (
	"context"
	"database/sql/driver"
	"path/filepath"
	"testing"

	sqlite3 "modernc.org/sqlite/lib"
)

func TestRuntime_Keywords(t *testing.T) {
	if n := KeywordCount(); n < 100 {
		t.Errorf("KeywordCount = %d, suspiciously low", n)
	}
	if !IsKeyword("SELECT") || !IsKeyword("select") {
		t.Error("SELECT should be a keyword (case-insensitive)")
	}
	if IsKeyword("notakeyword_xyz") {
		t.Error("notakeyword_xyz should not be a keyword")
	}
	if name, ok := KeywordName(0); !ok || name == "" {
		t.Errorf("KeywordName(0) = %q, %v; want a non-empty keyword", name, ok)
	}
	if _, ok := KeywordName(1 << 30); ok {
		t.Error("KeywordName past the end should return false")
	}
}

func TestRuntime_CompileOptions(t *testing.T) {
	if !CompileOptionUsed("ENABLE_FTS5") {
		t.Error("ENABLE_FTS5 should be a used compile option (FTS5 is exercised)")
	}
	if CompileOptionUsed("NONEXISTENT_OPTION_XYZ") {
		t.Error("a bogus compile option should report false")
	}
	if opt, ok := CompileOptionGet(0); !ok || opt == "" {
		t.Errorf("CompileOptionGet(0) = %q, %v", opt, ok)
	}
}

func TestRuntime_StringUtils(t *testing.T) {
	if !StrGlob("a*c", "abc") || StrGlob("a*c", "abd") {
		t.Error("StrGlob mismatch")
	}
	if !StrLike("a%c", "aXYZc", 0) || StrLike("a_c", "abbc", 0) {
		t.Error("StrLike mismatch")
	}
	if !Complete("SELECT 1;") || Complete("SELECT 1") {
		t.Error("Complete should be true only for a terminated statement")
	}
}

func TestConn_FilenameAndAutoCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.db")
	_, sc, c := withSQLite3Conn(t, path)
	ctx := context.Background()
	// SQLite canonicalizes the path (e.g. macOS /var → /private/var), so compare
	// resolved forms rather than the raw temp path.
	resolved, _ := filepath.EvalSymlinks(filepath.Dir(path))
	wantPath := filepath.Join(resolved, "f.db")
	if got := c.Filename("main"); got != path && got != wantPath {
		t.Errorf("Filename(main) = %q, want %q (or %q)", got, path, wantPath)
	}
	if got := c.Filename(""); got == "" {
		t.Error(`Filename("") returned empty; should default to main`)
	}
	if !c.AutoCommit() {
		t.Error("AutoCommit should be true at rest")
	}
	tx, err := sc.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.AutoCommit() {
		t.Error("AutoCommit should be false inside a transaction")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !c.AutoCommit() {
		t.Error("AutoCommit should be true after rollback")
	}

	_, _, cmem := withSQLite3Conn(t, ":memory:")
	if got := cmem.Filename("main"); got != "" {
		t.Errorf("in-memory Filename = %q, want empty", got)
	}
}

func TestConn_ErrorOffset(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	// A bad token (not an EOF "incomplete input") carries a byte offset — the
	// "FORM" typo starts at offset 9.
	if _, err := sc.ExecContext(ctx, `SELECT * FORM t`); err == nil {
		t.Fatal("expected a syntax error")
	}
	if off := c.ErrorOffset(); off < 0 {
		t.Errorf("ErrorOffset = %d, want >= 0 after a tokenized parse error", off)
	}
}

func TestConn_CacheFlushAndFileControl(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	_, sc, c := withSQLite3Conn(t, path)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.CacheFlush(); err != nil {
		t.Errorf("CacheFlush: %v", err)
	}
	if _, err := c.SetFileControlInt("main", sqlite3.SQLITE_FCNTL_CHUNK_SIZE, 4096); err != nil {
		t.Errorf("SetFileControlInt(CHUNK_SIZE): %v", err)
	}
	if err := c.ResetCache("main"); err != nil {
		t.Errorf("ResetCache: %v", err)
	}
}

// TestRegisterFunc_InnocuousFlag: an INNOCUOUS function stays usable from a view
// under trusted_schema=OFF, where a non-innocuous one is rejected as "unsafe".
func TestRegisterFunc_InnocuousFlag(t *testing.T) {
	one := func(*FunctionContext, []driver.Value) (driver.Value, error) { return int64(1), nil }
	if err := RegisterFunction("flag_ni", &FunctionImpl{NArgs: 0, Scalar: one}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterFunction("flag_inn", &FunctionImpl{NArgs: 0, Innocuous: true, Scalar: one}); err != nil {
		t.Fatal(err)
	}
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE VIEW v_ni AS SELECT flag_ni() AS x`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `CREATE VIEW v_inn AS SELECT flag_inn() AS x`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetDBConfig(DBConfigTrustedSchema, false); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := sc.QueryRowContext(ctx, `SELECT x FROM v_ni`).Scan(&n); err == nil {
		t.Error("non-innocuous function in a view should be rejected under trusted_schema=OFF")
	}
	if err := sc.QueryRowContext(ctx, `SELECT x FROM v_inn`).Scan(&n); err != nil {
		t.Errorf("innocuous function in a view should be allowed under trusted_schema=OFF: %v", err)
	}
}
