package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"testing"
)

// nestedPrepareTable is a vtab that, on every Filter call, prepares and
// runs `SELECT 1` against its host connection. The test pins the
// invariant the Tier-3 vtab ports (ext/closure, ext/pivot, ext/statement)
// all rely on: calling (*Conn).Prepare from inside an xFilter trampoline
// must succeed without panicking, deadlocking, or leaking stmts.
type nestedPrepareTable struct {
	c    *Conn
	rows int // produced rows per scan
}

func (nestedPrepareTable) BestIndex(info *IndexInfo) error {
	info.EstimatedCost = 1
	info.EstimatedRows = 1
	return nil
}

func (t nestedPrepareTable) Open() (VTabCursor, error) {
	return &nestedPrepareCursor{c: t.c, rows: t.rows}, nil
}

func (nestedPrepareTable) Disconnect() error { return nil }
func (nestedPrepareTable) Destroy() error    { return nil }

type nestedPrepareCursor struct {
	c    *Conn
	rows int
	row  int
}

func (nc *nestedPrepareCursor) Filter(int, string, []Value) error {
	// Reentrant Prepare from inside an xFilter trampoline — the load-
	// bearing invariant for ext/closure, ext/pivot, ext/statement.
	st, err := nc.c.Prepare("SELECT 1")
	if err != nil {
		return err
	}
	defer st.Close()
	rs, err := st.(driver.StmtQueryContext).QueryContext(context.Background(), nil)
	if err != nil {
		return err
	}
	defer rs.Close()
	dest := make([]driver.Value, 1)
	if err := rs.Next(dest); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	nc.row = 1
	return nil
}

func (nc *nestedPrepareCursor) Next() error { nc.row++; return nil }
func (nc *nestedPrepareCursor) Eof() bool   { return nc.row > nc.rows }
func (nc *nestedPrepareCursor) Column(int) (Value, error) {
	return int64(nc.row), nil
}
func (nc *nestedPrepareCursor) Rowid() (int64, error) { return int64(nc.row), nil }
func (nc *nestedPrepareCursor) Close() error          { return nil }

// TestVTab_NestedPrepareFromFilter pins that (*Conn).Prepare is reentrant
// from inside a vtab xFilter trampoline. The three Tier-3 vtab ports
// (closure / pivot / statement) compile child SQL inside xCreate / xFilter
// and would silently break if this stopped working after a modernc bump.
func TestVTab_NestedPrepareFromFilter(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	if err := c.CreateModule("np", func(cc *Conn, _, _, _ string, _ []string) (VTab, error) {
		if err := cc.DeclareVTab(`CREATE TABLE x(v INTEGER)`); err != nil {
			return nil, err
		}
		return nestedPrepareTable{c: cc, rows: 3}, nil
	}); err != nil {
		t.Fatalf("CreateModule: %v", err)
	}
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE n USING np()`); err != nil {
		t.Fatalf("CREATE VIRTUAL TABLE: %v", err)
	}

	// Run the SELECT a few times to ensure repeated reentry is safe.
	for i := range 3 {
		rows, err := sc.QueryContext(ctx, `SELECT v FROM n`)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		count := 0
		for rows.Next() {
			var v int64
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				t.Fatalf("Scan: %v", err)
			}
			count++
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("rows.Err: %v", err)
		}
		rows.Close()
		if count != 3 {
			t.Errorf("iter %d: got %d rows, want 3", i, count)
		}
	}
}
