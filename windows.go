package sqlite

import (
	"database/sql/driver"
	"fmt"
)

// WindowAccumulator is the interface a window-function implementation
// satisfies. SQLite calls Step as the engine moves forward through
// rows, Inverse as the frame's trailing edge moves past a row (so the
// row's contribution can be removed), and Value to read the current
// frame value.
//
// All three methods receive a [FunctionContext]; values are not valid
// past the return of each call. A fresh WindowAccumulator is created
// per query via the constructor passed to [Conn.RegisterWindowFunction],
// so per-instance state can be held on the receiver without external
// synchronization.
//
// For per-instance cleanup at the end of an aggregation, implement
// [WindowFinalizer] additionally — the adapter type-asserts and calls
// Final when present. Implementations without resources to release
// can omit it.
//
// For a non-window aggregate, implement Inverse as a no-op (or return
// an error if the function is genuinely non-invertible — SQLite will
// then fall back to whole-frame recomputation in some plans).
type WindowAccumulator interface {
	// Step incorporates one row's args into the accumulator.
	Step(ctx *FunctionContext, args []driver.Value) error
	// Inverse undoes the contribution of one row that previously went
	// through Step. The args match those passed to the original Step.
	Inverse(ctx *FunctionContext, args []driver.Value) error
	// Value returns the current frame value. Called once per output
	// row when the function is invoked as a window function.
	Value(ctx *FunctionContext) (driver.Value, error)
}

// WindowFinalizer is an optional companion interface a
// [WindowAccumulator] can implement to receive a Final callback after
// the last row of an aggregation. SQLite uses [WindowAccumulator.Value]
// to read the result; Final exists for any per-instance cleanup. The
// adapter calls Final only when the accumulator satisfies this
// interface, so most accumulators (pure-math ones with no external
// state) can leave it off.
type WindowFinalizer interface {
	Final(ctx *FunctionContext)
}

// RegisterWindowFunction registers a Go window-function implementation
// on this connection only. Each invocation of the function in SQL
// creates a fresh accumulator via constructor() and then drives it
// through SQLite's standard Step / Inverse / Value / Final lifecycle.
//
// Setting pure=true marks the function as deterministic
// (SQLITE_DETERMINISTIC), enabling SQLite to use it inside indexes and
// to fold its result for repeated invocations with identical
// arguments. Most accumulators are pure; pass false if the
// implementation reads external state (clock, RNG, env).
//
// nArg is the number of arguments the SQL function takes; pass -1 for
// variadic.
//
// # Why nArg is explicit (vs reflective like RegisterAggregator)
//
// [Conn.RegisterAggregator] infers argument count by reflecting over
// the Step method's signature: it accepts a typed value (e.g.
// *myAggregator with `Step(a, b float64)`), so the arity comes from
// reflect.Type.NumIn(). RegisterWindowFunction can't do that — the
// constructor returns a [WindowAccumulator] interface, whose Step
// method has the fixed signature `Step(*FunctionContext, []driver.Value)`,
// from which no per-arg information is recoverable. Callers therefore
// pass nArg explicitly. This is intentional: window functions
// frequently want variadic / multi-typed arg lists (which reflection
// couldn't express anyway), and the explicit form keeps the API
// honest about what SQLite needs.
//
// Example: a running-sum window function. Final is omitted — pure
// math doesn't need cleanup, and the accumulator is GC'd along with
// the rest of the query state.
//
//	type sumState struct{ total float64 }
//
//	func (s *sumState) Step(_ *sqlite.FunctionContext, a []driver.Value) error {
//	    s.total += a[0].(float64)
//	    return nil
//	}
//	func (s *sumState) Inverse(_ *sqlite.FunctionContext, a []driver.Value) error {
//	    s.total -= a[0].(float64)
//	    return nil
//	}
//	func (s *sumState) Value(_ *sqlite.FunctionContext) (driver.Value, error) {
//	    return s.total, nil
//	}
//
//	conn.RegisterWindowFunction("running_sum", 1,
//	    func() sqlite.WindowAccumulator { return &sumState{} }, true)
func (c *Conn) RegisterWindowFunction(name string, nArg int, constructor func() WindowAccumulator, pure bool) error {
	if constructor == nil {
		return fmt.Errorf("RegisterWindowFunction %q: constructor must not be nil", name)
	}
	factory := func(_ FunctionContext) (AggregateFunction, error) {
		return &windowAdapter{impl: constructor()}, nil
	}
	return c.registerAggregateFunction(name, int32(nArg), pure, factory)
}

// windowAdapter bridges WindowAccumulator (user-facing, explicit
// Step/Inverse/Value/Final) to AggregateFunction (internal interface
// SQLite's window-function trampoline already speaks).
type windowAdapter struct {
	impl WindowAccumulator
}

func (a *windowAdapter) Step(ctx *FunctionContext, rowArgs []driver.Value) error {
	return a.impl.Step(ctx, rowArgs)
}

func (a *windowAdapter) WindowInverse(ctx *FunctionContext, rowArgs []driver.Value) error {
	return a.impl.Inverse(ctx, rowArgs)
}

func (a *windowAdapter) WindowValue(ctx *FunctionContext) (driver.Value, error) {
	return a.impl.Value(ctx)
}

func (a *windowAdapter) Final(ctx *FunctionContext) {
	if f, ok := a.impl.(WindowFinalizer); ok {
		f.Final(ctx)
	}
}
