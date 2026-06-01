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
