package sqlite

import (
	"context"
	"database/sql"
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
