package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// winRunningSum is a simple invertible window accumulator: it tracks the
// total of values fed through Step and undoes contributions through
// Inverse. Used across the window-function tests as the canonical
// reference shape.
type winRunningSum struct {
	total float64
}

func (s *winRunningSum) Step(_ *FunctionContext, args []driver.Value) error {
	v, ok := args[0].(float64)
	if !ok {
		if i, ok := args[0].(int64); ok {
			v = float64(i)
		} else {
			return errors.New("winRunningSum: arg is not numeric")
		}
	}
	s.total += v
	return nil
}

func (s *winRunningSum) Inverse(_ *FunctionContext, args []driver.Value) error {
	v, ok := args[0].(float64)
	if !ok {
		if i, ok := args[0].(int64); ok {
			v = float64(i)
		} else {
			return errors.New("winRunningSum: arg is not numeric")
		}
	}
	s.total -= v
	return nil
}

func (s *winRunningSum) Value(_ *FunctionContext) (driver.Value, error) {
	return s.total, nil
}

func (s *winRunningSum) Final(_ *FunctionContext) {}

// TestRegisterWindowFunction_SlidingFrame exercises the headline use
// case: an aggregate driven over a moving window where Step adds
// rows entering the frame and Inverse removes rows leaving it. The
// frame here is the current row plus the prior one, so each output
// row sees a two-element sum.
func TestRegisterWindowFunction_SlidingFrame(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	if err := c.RegisterWindowFunction("rsum", 1,
		func() WindowAccumulator { return &winRunningSum{} }, true); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, v REAL)`); err != nil {
		t.Fatal(err)
	}
	for i, v := range []float64{10, 20, 30, 40, 50} {
		if _, err := sc.ExecContext(ctx, `INSERT INTO t (id, v) VALUES (?, ?)`, i+1, v); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := sc.QueryContext(ctx, `
        SELECT id, rsum(v) OVER (ORDER BY id ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)
        FROM t ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []float64{10, 30, 50, 70, 90}
	var got []float64
	for rows.Next() {
		var id int64
		var sum float64
		if err := rows.Scan(&id, &sum); err != nil {
			t.Fatal(err)
		}
		got = append(got, sum)
	}
	if len(got) != len(want) {
		t.Fatalf("rows=%d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got=%v, want=%v", i, got[i], want[i])
		}
	}
}

// TestRegisterWindowFunction_PartitionBy confirms each partition gets
// an independent accumulator instance — the running sum for partition
// "a" must not bleed into partition "b".
func TestRegisterWindowFunction_PartitionBy(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	if err := c.RegisterWindowFunction("rsum", 1,
		func() WindowAccumulator { return &winRunningSum{} }, true); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, g TEXT, v REAL)`); err != nil {
		t.Fatal(err)
	}
	type row struct {
		g string
		v float64
	}
	for i, r := range []row{{"a", 1}, {"a", 2}, {"b", 100}, {"a", 3}, {"b", 200}} {
		if _, err := sc.ExecContext(ctx, `INSERT INTO t (id, g, v) VALUES (?, ?, ?)`,
			i+1, r.g, r.v); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := sc.QueryContext(ctx, `
        SELECT g, rsum(v) OVER (PARTITION BY g ORDER BY id) FROM t ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type out struct {
		g   string
		sum float64
	}
	var got []out
	for rows.Next() {
		var o out
		if err := rows.Scan(&o.g, &o.sum); err != nil {
			t.Fatal(err)
		}
		got = append(got, o)
	}
	// Partition a sees 1, 1+2=3, 1+2+3=6; partition b sees 100, 100+200=300.
	want := []out{{"a", 1}, {"a", 3}, {"b", 100}, {"a", 6}, {"b", 300}}
	if len(got) != len(want) {
		t.Fatalf("rows=%d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got=%+v, want=%+v", i, got[i], want[i])
		}
	}
}

// erroringStep returns an error from Step on every row.
type erroringStep struct{}

func (erroringStep) Step(*FunctionContext, []driver.Value) error {
	return errors.New("intentional step failure")
}
func (erroringStep) Inverse(*FunctionContext, []driver.Value) error { return nil }
func (erroringStep) Value(*FunctionContext) (driver.Value, error)   { return int64(0), nil }
func (erroringStep) Final(*FunctionContext)                         {}

// TestRegisterWindowFunction_StepErrorPropagates confirms an error
// from Step surfaces to the SQL caller rather than being silently
// dropped.
func TestRegisterWindowFunction_StepErrorPropagates(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	if err := c.RegisterWindowFunction("efail", 1,
		func() WindowAccumulator { return erroringStep{} }, true); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sc.ExecContext(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, v INTEGER)`)
	sc.ExecContext(ctx, `INSERT INTO t VALUES (1, 1)`)
	_, err := sc.QueryContext(ctx,
		`SELECT efail(v) OVER (ORDER BY id) FROM t`)
	// Some driver paths surface the Step error at Query time, others at
	// Scan time. Accept either; what we don't want is silent success.
	if err == nil {
		rows, qerr := sc.QueryContext(ctx, `SELECT efail(v) OVER (ORDER BY id) FROM t`)
		if qerr == nil {
			defer rows.Close()
			for rows.Next() {
				var x int64
				_ = rows.Scan(&x)
			}
			err = rows.Err()
		}
	}
	if err == nil {
		t.Fatal("expected Step error to surface to caller, got nil")
	}
	if !strings.Contains(err.Error(), "intentional step failure") {
		t.Errorf("error %q doesn't mention the Step failure", err.Error())
	}
}

// TestRegisterWindowFunction_NilConstructorRejected ensures the
// public API guards against the obvious misuse.
func TestRegisterWindowFunction_NilConstructorRejected(t *testing.T) {
	_, _, c := withMattnConn(t, ":memory:")
	err := c.RegisterWindowFunction("nope", 1, nil, true)
	if err == nil {
		t.Fatal("expected error on nil constructor, got nil")
	}
	if !strings.Contains(err.Error(), "constructor must not be nil") {
		t.Errorf("error %q doesn't mention nil constructor", err.Error())
	}
}
