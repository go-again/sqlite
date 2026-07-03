package sqlite

import (
	"context"
	"testing"
)

// twinsTable is a minimal eponymous table-valued function: one row, one
// column. Its only job is to let a ctor call DeclareVTab so the test can pin
// which connection that declaration targets.
type twinsTable struct{}

func (twinsTable) BestIndex(info *IndexInfo) error {
	info.EstimatedCost = 1
	info.EstimatedRows = 1
	return nil
}
func (twinsTable) Open() (VTabCursor, error) { return &twinsCursor{}, nil }
func (twinsTable) Disconnect() error         { return nil }
func (twinsTable) Destroy() error            { return nil }

type twinsCursor struct{ row int }

func (c *twinsCursor) Filter(int, string, []Value) error { c.row = 0; return nil }
func (c *twinsCursor) Next() error                       { c.row++; return nil }
func (c *twinsCursor) Eof() bool                         { return c.row >= 1 }
func (c *twinsCursor) Column(int) (Value, error)         { return int64(42), nil }
func (c *twinsCursor) Rowid() (int64, error)             { return int64(c.row), nil }
func (c *twinsCursor) Close() error                      { return nil }

// TestVTabCtor_UsesExecutingConn is a regression test for a cross-connection
// aliasing bug. A vtab module registered on every pooled connection — which is
// exactly what the ext/<name>/auto ConnectHooks do (generate_series, rtree,
// spellfix1, …) — kept a global record keyed by module name, overwritten by
// whichever connection registered it last. The [VTabCtor] adapter then handed
// the ctor that last connection's *Conn, so its DeclareVTab ran against the
// wrong db handle whenever the query landed on a different connection, and
// sqlite3_declare_vtab returned SQLITE_MISUSE (surfacing as a flaky
// "declare_vtab" failure under a server's connection pool).
//
// This reproduces it deterministically: register the same eponymous module on
// two distinct connections, then query the first (now-stale) one.
func TestVTabCtor_UsesExecutingConn(t *testing.T) {
	reg := func(c *Conn) error {
		return c.CreateEponymousModule("twinseries", func(cc *Conn, _, _, _ string, _ []string) (VTab, error) {
			if err := cc.DeclareVTab(`CREATE TABLE x(value INTEGER)`); err != nil {
				return nil, err
			}
			return twinsTable{}, nil
		})
	}

	// First connection registers the module — the global record now points at
	// c1's ctor adapter.
	_, sc1, c1 := withSQLite3Conn(t, ":memory:")
	if err := reg(c1); err != nil {
		t.Fatalf("register on c1: %v", err)
	}

	// A second, distinct connection registers the same-named module, so the
	// global record is overwritten to point at c2's ctor adapter.
	_, _, c2 := withSQLite3Conn(t, ":memory:")
	if err := reg(c2); err != nil {
		t.Fatalf("register on c2: %v", err)
	}

	// Query the eponymous table on the FIRST (now-stale) connection. xConnect
	// must run the ctor against c1 — the connection SQLite is actually driving
	// — not c2, or declare_vtab returns SQLITE_MISUSE.
	var got int64
	if err := sc1.QueryRowContext(context.Background(), `SELECT value FROM twinseries`).Scan(&got); err != nil {
		t.Fatalf("SELECT on stale connection: %v", err)
	}
	if got != 42 {
		t.Errorf("value = %d, want 42", got)
	}
}
