package sqlite

import (
	"database/sql"
	"database/sql/driver"
	"sync/atomic"
	"testing"
)

func auxDataLen() int {
	auxDataReg.mu.RLock()
	defer auxDataReg.mu.RUnlock()
	return len(auxDataReg.m)
}

// TestFunctionContext_Subtype round-trips a subtype through SQLite: one UDF tags
// its result, a second reads the tag off its argument. A broken ResultSubtype or
// ValueSubtype would surface 0 instead of the tag.
func TestFunctionContext_Subtype(t *testing.T) {
	const tag = 42
	if err := RegisterFunction("fc_set_sub", &FunctionImpl{
		NArgs: 1,
		Scalar: func(ctx *FunctionContext, args []driver.Value) (driver.Value, error) {
			ctx.ResultSubtype(tag)
			return args[0], nil
		},
	}); err != nil {
		t.Fatalf("RegisterFunction set: %v", err)
	}
	if err := RegisterFunction("fc_get_sub", &FunctionImpl{
		NArgs: 1,
		Scalar: func(ctx *FunctionContext, _ []driver.Value) (driver.Value, error) {
			return int64(ctx.ValueSubtype(0)), nil
		},
	}); err != nil {
		t.Fatalf("RegisterFunction get: %v", err)
	}

	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var got int
	if err := db.QueryRow(`SELECT fc_get_sub(fc_set_sub('x'))`).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != tag {
		t.Errorf("subtype round-trip = %d, want %d (ResultSubtype/ValueSubtype not wired)", got, tag)
	}
}

// TestFunctionContext_AuxData proves per-argument aux-data caching: a UDF with a
// constant argument computes once and reuses the cache for every later row, and
// the cached Go value is released (registry drained) once the statement is
// finalized.
func TestFunctionContext_AuxData(t *testing.T) {
	var computes atomic.Int64
	if err := RegisterFunction("fc_auxcache", &FunctionImpl{
		NArgs: 1,
		Scalar: func(ctx *FunctionContext, _ []driver.Value) (driver.Value, error) {
			if v, ok := ctx.GetAuxData(0); ok {
				return v, nil // cache hit — reuse without recomputing
			}
			computes.Add(1)
			val := int64(7)
			ctx.SetAuxData(0, val)
			return val, nil
		},
	}); err != nil {
		t.Fatalf("RegisterFunction: %v", err)
	}

	base := auxDataLen()

	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}

	// 50 rows, a constant argument: SQLite preserves the auxdata across rows, so
	// the body computes exactly once.
	rows, err := db.Query(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i < 50) SELECT fc_auxcache('const') FROM c`)
	if err != nil {
		db.Close()
		t.Fatalf("query: %v", err)
	}
	n := 0
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if v != 7 {
			t.Errorf("row %d = %d, want 7", n, v)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	rows.Close()

	if n != 50 {
		t.Errorf("rows = %d, want 50", n)
	}
	if c := computes.Load(); c != 1 {
		t.Errorf("computed %d times over 50 rows, want 1 (auxdata cache not honored)", c)
	}

	// Finalizing the statements (closing the DB) must fire the xDelete
	// destructor for every live auxdata entry, draining the registry.
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := auxDataLen(); got != base {
		t.Errorf("auxdata registry not drained after finalize: have %d, want %d (destructor leak)", got, base)
	}
}
