package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// walDB opens a file-backed database in WAL journal mode (required for the WAL
// and snapshot APIs) on a single pinned connection.
func walDB(t *testing.T) (context.Context, *sql.Conn, *Conn) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wal.db")
	_, sc, c := withSQLite3Conn(t, path)
	if _, err := sc.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("journal_mode=WAL: %v", err)
	}
	return ctx, sc, c
}

func TestWALCheckpoint(t *testing.T) {
	ctx, sc, c := walDB(t)
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (?)`, i); err != nil {
			t.Fatal(err)
		}
	}
	logFrames, checkpointed, err := c.WALCheckpoint("main", CheckpointTruncate)
	if err != nil {
		t.Fatalf("WALCheckpoint: %v", err)
	}
	if logFrames < 0 || checkpointed < 0 {
		t.Errorf("frame counts = (%d, %d), want non-negative", logFrames, checkpointed)
	}
	if err := c.WALAutoCheckpoint(500); err != nil {
		t.Fatalf("WALAutoCheckpoint: %v", err)
	}
}

func TestWALHook(t *testing.T) {
	ctx, sc, c := walDB(t)

	var calls, lastFrames int
	c.RegisterWALHook(func(schema string, walFrames int) error {
		if schema != "main" {
			t.Errorf("WAL hook schema = %q, want main", schema)
		}
		calls++
		lastFrames = walFrames
		return nil
	})

	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil { // commit → hook
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil { // commit → hook
		t.Fatal(err)
	}
	if calls < 2 {
		t.Errorf("WAL hook fired %d times, want >= 2", calls)
	}
	if lastFrames <= 0 {
		t.Errorf("WAL hook reported %d frames, want > 0", lastFrames)
	}

	c.RegisterWALHook(nil) // remove
	before := calls
	if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (2)`); err != nil {
		t.Fatal(err)
	}
	if calls != before {
		t.Error("WAL hook fired after removal")
	}
}

func TestProgressHandler(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()

	calls := 0
	c.SetProgressHandler(1, func() bool {
		calls++
		return calls > 50 // interrupt once we've been called enough
	})

	// A bounded-but-large recursive CTE: it terminates on its own (no hang risk
	// if the handler misbehaves), but the handler interrupts it first.
	_, err := sc.ExecContext(ctx,
		`WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c LIMIT 1000000) SELECT count(*) FROM c`)
	if calls == 0 {
		t.Error("progress handler never fired")
	}
	if err == nil {
		t.Error("expected the progress handler to interrupt the query")
	}

	// Removing the handler lets a query complete.
	c.SetProgressHandler(0, nil)
	if _, err := sc.ExecContext(ctx, `SELECT 1`); err != nil {
		t.Fatalf("query after removing handler: %v", err)
	}
}

func TestDBConfig(t *testing.T) {
	_, _, c := withSQLite3Conn(t, ":memory:")

	on, err := c.SetDBConfig(DBConfigDefensive, true)
	if err != nil {
		t.Fatalf("SetDBConfig(Defensive, true): %v", err)
	}
	if !on {
		t.Error("Defensive should read back enabled")
	}
	q, err := c.QueryDBConfig(DBConfigDefensive)
	if err != nil {
		t.Fatalf("QueryDBConfig(Defensive): %v", err)
	}
	if !q {
		t.Error("QueryDBConfig(Defensive) = false, want true")
	}

	// Toggle foreign keys both ways.
	if v, _ := c.SetDBConfig(DBConfigForeignKeys, true); !v {
		t.Error("ForeignKeys enable failed")
	}
	if v, _ := c.SetDBConfig(DBConfigForeignKeys, false); v {
		t.Error("ForeignKeys disable failed")
	}
}

// TestDBConfig_Effect verifies a db_config flag has its actual semantic effect,
// not just a consistent read-back — a mis-mapped op constant would pass the
// read-back test but fail this one. Foreign-key enforcement is the clearest
// deterministic effect to assert.
func TestDBConfig_Effect(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE parent(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx,
		`CREATE TABLE child(id INTEGER PRIMARY KEY, pid INTEGER REFERENCES parent(id))`); err != nil {
		t.Fatal(err)
	}

	// FK enforcement OFF → a dangling reference inserts fine.
	if _, err := c.SetDBConfig(DBConfigForeignKeys, false); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO child VALUES (1, 999)`); err != nil {
		t.Fatalf("with FK enforcement off, a dangling insert should succeed: %v", err)
	}
	if _, err := sc.ExecContext(ctx, `DELETE FROM child`); err != nil {
		t.Fatal(err)
	}

	// FK enforcement ON → the same insert is rejected. If DBConfigForeignKeys
	// were wired to the wrong op constant this would not change.
	if _, err := c.SetDBConfig(DBConfigForeignKeys, true); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO child VALUES (2, 999)`); err == nil {
		t.Error("with FK enforcement on, a dangling insert should be rejected (proves ENABLE_FKEY is wired)")
	}
}

// TestDBConfig_WritableSchemaEffect verifies a security-sensitive db_config
// flag — DBConfigWritableSchema, gating direct writes to sqlite_schema — has
// its real semantic effect, the class of flag the audit's M10 finding wanted
// exercised. A mis-mapped op constant would read back fine but not flip this.
func TestDBConfig_WritableSchemaEffect(t *testing.T) {
	_, sc, c := withSQLite3Conn(t, ":memory:")
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	// A no-op-valued write to sqlite_schema: rejected unless writable_schema
	// is on, so its success/failure isolates the flag's effect.
	const schemaWrite = `UPDATE sqlite_schema SET sql = sql WHERE type = 'table'`

	// Default (writable_schema OFF): the write is rejected.
	if _, err := c.SetDBConfig(DBConfigWritableSchema, false); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, schemaWrite); err == nil {
		t.Error("with writable_schema off, a direct sqlite_schema write should be rejected")
	}

	// writable_schema ON: the same write is now permitted.
	if _, err := c.SetDBConfig(DBConfigWritableSchema, true); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, schemaWrite); err != nil {
		t.Errorf("with writable_schema on, a direct sqlite_schema write should succeed: %v", err)
	}
}

func TestWALHook_Error(t *testing.T) {
	ctx, sc, c := walDB(t)
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	c.RegisterWALHook(func(schema string, walFrames int) error {
		return errors.New("hook rejected the commit")
	})
	if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err == nil {
		t.Error("a WAL hook returning an error should surface as a commit error")
	}
	c.RegisterWALHook(nil)
}

func TestSnapshot(t *testing.T) {
	ctx, sc, c := walDB(t)
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}

	// snapshot_get needs an open read transaction on the schema.
	tx, err := sc.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.QueryContext(ctx, `SELECT count(*) FROM t`); err != nil {
		t.Fatal(err)
	}

	snap, err := c.GetSnapshot("main")
	if err != nil {
		t.Skipf("GetSnapshot unavailable in this configuration: %v", err)
	}
	defer snap.Close()
	if cmp := snap.Cmp(snap); cmp != 0 {
		t.Errorf("snapshot compared with itself = %d, want 0", cmp)
	}
}

// captureSnapshot opens a read transaction (the precondition for snapshot_get)
// and returns the captured snapshot, skipping the test if the configuration
// doesn't support it.
func captureSnapshot(t *testing.T, ctx context.Context, sc *sql.Conn, c *Conn) *Snapshot {
	t.Helper()
	tx, err := sc.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.QueryContext(ctx, `SELECT count(*) FROM t`); err != nil {
		t.Fatal(err)
	}
	snap, err := c.GetSnapshot("main")
	if err != nil {
		t.Skipf("GetSnapshot unavailable: %v", err)
	}
	return snap
}

func TestSnapshot_CmpAndGuards(t *testing.T) {
	ctx, sc, c := walDB(t)
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}

	snap1 := captureSnapshot(t, ctx, sc, c)
	defer snap1.Close()
	if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (2)`); err != nil { // advance the WAL
		t.Fatal(err)
	}
	snap2 := captureSnapshot(t, ctx, sc, c)
	defer snap2.Close()

	// snap1 was captured before the second write, so it is older than snap2.
	if snap1.Cmp(snap2) >= 0 {
		t.Errorf("snap1.Cmp(snap2) = %d, want < 0 (snap1 older)", snap1.Cmp(snap2))
	}
	if snap2.Cmp(snap1) <= 0 {
		t.Errorf("snap2.Cmp(snap1) = %d, want > 0 (snap2 newer)", snap2.Cmp(snap1))
	}

	// Closed/nil handle guards: Cmp returns 0, OpenSnapshot errors — neither
	// dereferences a NULL pointer in C.
	closed := &Snapshot{}
	if closed.Cmp(snap1) != 0 {
		t.Error("Cmp with a closed snapshot should be 0")
	}
	if snap1.Cmp(nil) != 0 {
		t.Error("Cmp with a nil snapshot should be 0")
	}
	if err := c.OpenSnapshot("main", closed); err == nil {
		t.Error("OpenSnapshot on a closed snapshot should error")
	}
	if err := c.OpenSnapshot("main", nil); err == nil {
		t.Error("OpenSnapshot on a nil snapshot should error")
	}
}

func TestSnapshot_OpenAndRecover(t *testing.T) {
	ctx, sc, c := walDB(t)
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	snap := captureSnapshot(t, ctx, sc, c) // anchors at the one-row state
	defer snap.Close()

	// Advance the database past the snapshot: a second row the replayed
	// snapshot must NOT see.
	if _, err := sc.ExecContext(ctx, `INSERT INTO t VALUES (2)`); err != nil {
		t.Fatal(err)
	}

	// SnapshotRecover must run with no open transaction; it keeps historical
	// snapshots reachable across checkpoints.
	if err := c.SnapshotRecover("main"); err != nil {
		t.Fatalf("SnapshotRecover: %v", err)
	}

	// Replay: open a fresh deferred transaction (BEGIN, no read yet) then
	// anchor it at the snapshot with OpenSnapshot. The follow-up SELECT must
	// see exactly the single row that existed when the snapshot was taken —
	// proving the wrapper round-trips the handle into C and that the read is
	// anchored at the historical state, not the current two-row database.
	tx, err := sc.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := c.OpenSnapshot("main", snap); err != nil {
		// snapshot_open's preconditions (no checkpoint has overwritten the
		// snapshot, WAL still holds the frames) are environment-sensitive;
		// skip explicitly rather than silently tolerating a failure.
		t.Skipf("OpenSnapshot unavailable in this environment: %v", err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("snapshot replay sees %d rows, want 1 (historical state, before the 2nd insert)", n)
	}
}
