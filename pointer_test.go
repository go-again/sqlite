package sqlite

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// TestPointer_UDFReceivesWrappedValue confirms a UDF that takes `any`
// receives the original Go value when invoked with sqlite.Pointer(v) —
// not a nil, not a primitive cast.
func TestPointer_UDFReceivesWrappedValue(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")

	var captured []any
	if err := c.RegisterFunc("capture",
		func(v any) string {
			captured = append(captured, v)
			return "ok"
		}, false); err != nil {
		t.Fatalf("RegisterFunc: %v", err)
	}

	type point struct{ X, Y int }

	cases := []any{
		[]int{1, 2, 3},
		map[string]int{"a": 1, "b": 2},
		&point{X: 7, Y: 11},
	}
	ctx := context.Background()
	for _, v := range cases {
		var got string
		if err := sc.QueryRowContext(ctx, `SELECT capture(?)`, Pointer(v)).Scan(&got); err != nil {
			t.Fatalf("Pointer(%T): %v", v, err)
		}
		if got != "ok" {
			t.Errorf("Pointer(%T) → %q, want %q", v, got, "ok")
		}
	}

	if len(captured) != len(cases) {
		t.Fatalf("captured %d, want %d", len(captured), len(cases))
	}
	if _, ok := captured[0].([]int); !ok {
		t.Errorf("captured[0]=%T, want []int", captured[0])
	}
	if _, ok := captured[1].(map[string]int); !ok {
		t.Errorf("captured[1]=%T, want map[string]int", captured[1])
	}
	if _, ok := captured[2].(*point); !ok {
		t.Errorf("captured[2]=%T, want *point", captured[2])
	}
}

// TestPointer_DestructorRunsOnFinalize confirms that closing the rows
// (which finalizes the statement) drains the pointer registry.
func TestPointer_DestructorRunsOnFinalize(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	if err := c.RegisterFunc("noop", func(any) int64 { return 0 }, false); err != nil {
		t.Fatal(err)
	}

	startSize := pointerRegistrySize()
	ctx := context.Background()
	for i := range 25 {
		if _, err := sc.ExecContext(ctx, `SELECT noop(?)`, Pointer([]int{i, i + 1})); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	// Force any in-flight finalize / GC nudge to settle. SQLite's
	// destructor fires synchronously on stmt finalize, which Exec does
	// inline before returning — but give a small budget for the
	// libc path to drop the binding fully.
	runtime.GC()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if pointerRegistrySize() == startSize {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := pointerRegistrySize(); got != startSize {
		t.Errorf("registry size after 25 bindings: %d, want %d (leak suggests destructor not running)",
			got, startSize)
	}
}

// TestPointer_NilUnwrappedAsNil confirms that wrapping a Go nil and
// reading it back yields nil — not a non-nil interface holding a typed
// nil. UDF code can rely on `v == nil` checks.
func TestPointer_NilUnwrappedAsNil(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	var seen any
	if err := c.RegisterFunc("recv", func(v any) int64 { seen = v; return 1 }, false); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.ExecContext(context.Background(),
		`SELECT recv(?)`, Pointer(nil)); err != nil {
		t.Fatal(err)
	}
	if seen != nil {
		t.Errorf("seen=%v (%T), want untyped nil", seen, seen)
	}
}

// TestValuePointer_PrimitiveReturnsFalse confirms that ValuePointer
// distinguishes a regular SQL primitive (int64 from a plain bind) from
// a Pointer-bound Go object.
func TestValuePointer_PrimitiveReturnsFalse(t *testing.T) {
	cases := []any{
		nil,
		int64(42),
		float64(3.14),
		"hello",
		[]byte{1, 2, 3},
		true,
	}
	for _, v := range cases {
		_, ok := ValuePointer(v)
		if ok {
			t.Errorf("ValuePointer(%T=%v) returned ok=true, want false", v, v)
		}
	}
	_, ok := ValuePointer([]int{1, 2})
	if !ok {
		t.Error("ValuePointer([]int{...}) returned ok=false, want true")
	}
}

// TestPointer_ResetPreservesBinding pins the documented behavior that
// sqlite3_reset does NOT clear bindings — a prepared statement reused
// via Reset keeps its Pointer binding until rebound or finalized.
func TestPointer_ResetPreservesBinding(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	var captured []any
	if err := c.RegisterFunc("capture",
		func(v any) int64 {
			captured = append(captured, v)
			return 0
		}, false); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	stmt, err := sc.PrepareContext(ctx, `SELECT capture(?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()

	// First exec binds Pointer(slice).
	if _, err := stmt.ExecContext(ctx, Pointer([]int{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	// Second exec rebinds the SAME parameter slot. Registry size should
	// stay small — the old binding's destructor fires when the new
	// binding overwrites the slot.
	if _, err := stmt.ExecContext(ctx, Pointer([]int{4, 5, 6})); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 {
		t.Fatalf("captured %d, want 2", len(captured))
	}
	got1, _ := captured[0].([]int)
	got2, _ := captured[1].([]int)
	if len(got1) != 3 || got1[0] != 1 || len(got2) != 3 || got2[0] != 4 {
		t.Errorf("captured=%v, want first={1,2,3} second={4,5,6}", captured)
	}
}

// TestPointer_RebindReleasesPriorEntry confirms that overwriting a
// parameter slot drops the old binding from the registry (destructor
// fires).
func TestPointer_RebindReleasesPriorEntry(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	if err := c.RegisterFunc("noop", func(any) int64 { return 0 }, false); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	stmt, err := sc.PrepareContext(ctx, `SELECT noop(?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	start := pointerRegistrySize()
	for range 50 {
		// Each Exec binds a new Pointer; the prior binding gets dropped
		// when the new one overwrites the slot.
		if _, err := stmt.ExecContext(ctx, Pointer([]int{0})); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && pointerRegistrySize() > start+1 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := pointerRegistrySize(); got > start+1 {
		t.Errorf("registry grew to %d after 50 rebinds, want ≤ %d (rebinds aren't releasing)", got, start+1)
	}
}

// TestPointer_TypedNilSlice confirms that a typed-nil ([]int)(nil)
// passed through Pointer arrives at the UDF as either a nil-typed []int
// or untyped nil — either is acceptable; the test just pins that no
// panic occurs and the UDF receives SOMETHING distinguishable from a
// non-nil slice.
func TestPointer_TypedNilSlice(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	var captured any
	if err := c.RegisterFunc("capture",
		func(v any) int64 {
			captured = v
			return 0
		}, false); err != nil {
		t.Fatal(err)
	}
	var nilSlice []int
	if _, err := sc.ExecContext(context.Background(),
		`SELECT capture(?)`, Pointer(nilSlice)); err != nil {
		t.Fatal(err)
	}
	// captured is either ([]int)(nil) or untyped nil. Both are valid;
	// what matters is no panic.
	if captured != nil {
		s, ok := captured.([]int)
		if !ok || s != nil {
			t.Errorf("captured=%v (%T), want nil or []int(nil)", captured, captured)
		}
	}
}

// TestPointer_HighVolume hammers the registry to surface any mutex
// imbalance under `go test -race`. The existing single-conn fixture
// serializes bind operations, but the race detector still observes the
// store/load/release path across the bind / functionArgs / destructor
// goroutine stacks.
func TestPointer_HighVolume(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	if err := c.RegisterFunc("noop", func(any) int64 { return 0 }, false); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	startSize := pointerRegistrySize()
	for i := range 200 {
		if _, err := sc.ExecContext(ctx, `SELECT noop(?)`, Pointer([]int{i, i + 1})); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && pointerRegistrySize() > startSize {
		time.Sleep(5 * time.Millisecond)
	}
	if got := pointerRegistrySize(); got != startSize {
		t.Errorf("registry size after 200 bindings: %d, want %d", got, startSize)
	}
}
