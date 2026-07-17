package sqlite

import (
	"context"
	"database/sql/driver"
	"slices"
	"sync"
	"testing"
)

// TestStmtExplain drives sqlite3_stmt_explain / _isexplain: a prepared statement
// flips between normal, EXPLAIN, and EXPLAIN QUERY PLAN mode at runtime, and
// stepping it in EXPLAIN mode yields the bytecode program (an "opcode" column).
func TestStmtExplain(t *testing.T) {
	_, _, c := withSQLite3Conn(t, ":memory:")
	dstmt, err := c.Prepare("SELECT 1 WHERE 1 = ?")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer dstmt.Close()
	st := dstmt.(*stmt)

	if got := st.IsExplain(); got != ExplainOff {
		t.Fatalf("fresh statement IsExplain = %d, want ExplainOff", got)
	}
	// Mode flips on a reset (never-stepped) statement are free and reversible.
	for _, m := range []ExplainMode{ExplainFull, ExplainQueryPlan, ExplainOff} {
		if err := st.Explain(m); err != nil {
			t.Fatalf("Explain(%d): %v", m, err)
		}
		if got := st.IsExplain(); got != m {
			t.Fatalf("after Explain(%d), IsExplain = %d", m, got)
		}
	}

	// Stepping in EXPLAIN mode yields the VM program, not the query's row.
	if err := st.Explain(ExplainFull); err != nil {
		t.Fatalf("Explain(Full): %v", err)
	}
	rows, err := dstmt.(driver.StmtQueryContext).QueryContext(
		context.Background(), []driver.NamedValue{{Ordinal: 1, Value: int64(1)}})
	if err != nil {
		t.Fatalf("query explained statement: %v", err)
	}
	cols := rows.Columns()
	_ = rows.Close()
	if !slices.Contains(cols, "opcode") {
		t.Fatalf("EXPLAIN columns = %v, want an \"opcode\" column", cols)
	}

	// Finalized statement: the pstmt==0 guards must return cleanly, not deref a
	// freed handle. IsExplain reports ExplainOff; Explain errors.
	fin, err := c.Prepare("SELECT 1")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	_ = fin.Close()
	fs := fin.(*stmt)
	if got := fs.IsExplain(); got != ExplainOff {
		t.Fatalf("finalized-statement IsExplain = %d, want ExplainOff", got)
	}
	if err := fs.Explain(ExplainFull); err == nil {
		t.Fatal("Explain on a finalized statement should error, got nil")
	}
}

// TestOverloadFunction pins the contract of sqlite3_overload_function: a name
// unknown before the overload is rejected at prepare time; after it, the name is
// recognized at prepare time (so a virtual table's xFindFunction could bind it).
func TestOverloadFunction(t *testing.T) {
	_, _, c := withSQLite3Conn(t, ":memory:")
	if _, err := c.Prepare("SELECT quux(1)"); err == nil {
		t.Fatal("prepare of an unknown function should fail before OverloadFunction")
	}
	if err := c.OverloadFunction("quux", 1); err != nil {
		t.Fatalf("OverloadFunction: %v", err)
	}
	st, err := c.Prepare("SELECT quux(1)")
	if err != nil {
		t.Fatalf("prepare after OverloadFunction should succeed: %v", err)
	}
	_ = st.Close()
}

// distinctProbe records the VTabDistinct mode observed inside BestIndex.
var distinctProbe struct {
	mu     sync.Mutex
	called bool
	modes  []VTabDistinctMode
}

type distinctTable struct{}

func (distinctTable) BestIndex(info *IndexInfo) error {
	m := VTabDistinct(info)
	distinctProbe.mu.Lock()
	distinctProbe.called = true
	distinctProbe.modes = append(distinctProbe.modes, m)
	distinctProbe.mu.Unlock()
	info.EstimatedCost = 1
	info.EstimatedRows = 2
	return nil
}
func (distinctTable) Open() (VTabCursor, error) { return &distinctCursor{}, nil }
func (distinctTable) Disconnect() error         { return nil }
func (distinctTable) Destroy() error            { return nil }

type distinctCursor struct{ row int }

func (c *distinctCursor) Filter(int, string, []Value) error { c.row = 0; return nil }
func (c *distinctCursor) Next() error                       { c.row++; return nil }
func (c *distinctCursor) Eof() bool                         { return c.row >= 3 }
func (c *distinctCursor) Column(int) (Value, error)         { return int64(c.row % 2), nil }
func (c *distinctCursor) Rowid() (int64, error)             { return int64(c.row), nil }
func (c *distinctCursor) Close() error                      { return nil }

// TestVTabDistinct exercises sqlite3_vtab_distinct via VTabDistinct: it returns
// the safe default outside a BestIndex call, and a valid mode when called from
// inside one.
func TestVTabDistinct(t *testing.T) {
	// Outside a BestIndex invocation, the raw index_info is unavailable → default.
	if got := VTabDistinct(&IndexInfo{}); got != VTabDistinctOrdered {
		t.Fatalf("VTabDistinct outside BestIndex = %d, want VTabDistinctOrdered", got)
	}

	distinctProbe.called = false
	distinctProbe.modes = nil

	_, sc, c := withSQLite3Conn(t, ":memory:")
	if err := c.CreateEponymousModule("distvt", func(cc *Conn, _, _, _ string, _ []string) (VTab, error) {
		if err := cc.DeclareVTab(`CREATE TABLE x(v INTEGER)`); err != nil {
			return nil, err
		}
		return distinctTable{}, nil
	}); err != nil {
		t.Fatalf("CreateEponymousModule: %v", err)
	}

	rows, err := sc.QueryContext(context.Background(), `SELECT DISTINCT v FROM distvt ORDER BY v`)
	if err != nil {
		t.Fatalf("query distvt: %v", err)
	}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
	}
	rows.Close()

	distinctProbe.mu.Lock()
	defer distinctProbe.mu.Unlock()
	if !distinctProbe.called {
		t.Fatal("BestIndex was never called")
	}
	for _, m := range distinctProbe.modes {
		if m < VTabDistinctOrdered || m > VTabDistinctUnordered {
			t.Fatalf("VTabDistinct returned %d, out of range [0,3]", m)
		}
	}
}

// findFuncTable is a 1..5 eponymous table that overloads a `triple` function via
// VTabFunctionFinder (xFindFunction).
type findFuncTable struct{}

func (findFuncTable) BestIndex(info *IndexInfo) error { info.EstimatedCost = 5; return nil }
func (findFuncTable) Open() (VTabCursor, error)       { return &findFuncCursor{}, nil }
func (findFuncTable) Disconnect() error               { return nil }
func (findFuncTable) Destroy() error                  { return nil }

func (findFuncTable) FindFunction(nArg int, name string) (func(*FunctionContext, []Value) (Value, error), int, bool) {
	if name == "triple" && nArg == 1 {
		return func(_ *FunctionContext, args []Value) (Value, error) {
			n, _ := args[0].(int64)
			return n * 3, nil
		}, 0, true
	}
	return nil, 0, false
}

type findFuncCursor struct{ row int }

func (c *findFuncCursor) Filter(int, string, []Value) error { c.row = 1; return nil }
func (c *findFuncCursor) Next() error                       { c.row++; return nil }
func (c *findFuncCursor) Eof() bool                         { return c.row > 5 }
func (c *findFuncCursor) Column(int) (Value, error)         { return int64(c.row), nil }
func (c *findFuncCursor) Rowid() (int64, error)             { return int64(c.row), nil }
func (c *findFuncCursor) Close() error                      { return nil }

// TestVTabFindFunction pins the xFindFunction path: a module overloads a function
// applied to its columns via VTabFunctionFinder, and OverloadFunction makes the
// name prepare-able. Running the query twice exercises the per-table id cache.
func TestVTabFindFunction(t *testing.T) {
	db, sc, c := withSQLite3Conn(t, ":memory:")
	sizeOf := func() int { xFuncs.mu.Lock(); defer xFuncs.mu.Unlock(); return len(xFuncs.m) }
	before := sizeOf()
	if err := c.CreateEponymousModule("findfuncvt", func(cc *Conn, _, _, _ string, _ []string) (VTab, error) {
		if err := cc.DeclareVTab(`CREATE TABLE x(value INTEGER)`); err != nil {
			return nil, err
		}
		return findFuncTable{}, nil
	}); err != nil {
		t.Fatalf("CreateEponymousModule: %v", err)
	}
	if err := c.OverloadFunction("triple", 1); err != nil {
		t.Fatalf("OverloadFunction: %v", err)
	}

	run := func() []int64 {
		rows, err := sc.QueryContext(context.Background(), `SELECT triple(value) FROM findfuncvt ORDER BY value`)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		defer rows.Close()
		var got []int64
		for rows.Next() {
			var v int64
			if err := rows.Scan(&v); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got = append(got, v)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		return got
	}

	want := []int64{3, 6, 9, 12, 15}
	for i := range 2 { // twice: the second prepare reuses the cached override id
		got := run()
		if !slices.Equal(got, want) {
			t.Fatalf("run %d: triple(value) over 1..5 = %v, want %v", i, got, want)
		}
	}

	// Cache reuse: xFindFunction fires per-prepare, but the per-(name,nArg) cache
	// must mint exactly ONE override id across both prepares — not one per prepare
	// (the leak the cache exists to prevent). Without the cache this delta is 2.
	if got := sizeOf() - before; got != 1 {
		t.Fatalf("xFindFunction minted %d override ids across two prepares, want exactly 1", got)
	}

	// Free on disconnect: closing the pool disconnects the eponymous vtab, which
	// must release the override id (both the xFuncs.m entry and its generator bit),
	// returning the registry to its baseline size.
	_ = sc.Close()
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}
	if got := sizeOf(); got != before {
		t.Fatalf("after disconnect xFuncs.m has %d entries, want baseline %d (override id not freed)", got, before)
	}
}
