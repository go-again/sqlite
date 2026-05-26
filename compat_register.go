package sqlite

import (
	"database/sql/driver"
	"fmt"
	"reflect"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// RegisterFunc registers a Go function as a SQLite scalar function for this
// connection only.
//
// Mattn-compatibility API: the function impl may have any signature whose
// argument and return types are supported. Supported argument types are
// int*, uint*, float64, bool, string, []byte, time.Time, and any (which
// receives the value as the corresponding Go type from the SQLite value).
// The function may return a single value, or (value, error). The last
// argument may be variadic.
//
// Setting pure=true marks the function as deterministic (SQLITE_DETERMINISTIC),
// allowing SQLite to use it inside indexes and to fold its result for repeated
// invocations with identical arguments.
//
// The function is registered on this connection only, mirroring mattn's
// per-connection registration model.
func (c *Conn) RegisterFunc(name string, impl any, pure bool) error {
	scalar, nArg, err := reflectScalar(impl)
	if err != nil {
		return fmt.Errorf("RegisterFunc %q: %w", name, err)
	}
	return c.registerScalarFunction(name, nArg, pure, scalar)
}

// RegisterAggregator registers a Go type with Step and Done methods as a SQLite
// aggregate function for this connection only.
//
// Mattn-compatibility API: the impl argument must be a function that returns a
// new instance of an aggregate type. Each invocation of the aggregate at SQL
// query time creates one instance via impl(), then calls its Step(...) for each
// row and its Done() at the end. Step's argument types follow the same rules
// as RegisterFunc; Done returns the aggregate value, optionally with an error.
//
// Setting pure=true marks the aggregate as deterministic.
func (c *Conn) RegisterAggregator(name string, impl any, pure bool) error {
	factory, nArg, err := reflectAggregator(impl)
	if err != nil {
		return fmt.Errorf("RegisterAggregator %q: %w", name, err)
	}
	return c.registerAggregateFunction(name, nArg, pure, factory)
}

// RegisterCollation registers a Go comparator as a named SQLite collation for
// this connection only. The comparator must obey the standard sort contract:
// negative if a < b, zero if a == b, positive if a > b.
//
// Mattn-compatibility API equivalent to SQLiteConn.RegisterCollation.
func (c *Conn) RegisterCollation(name string, cmp func(a, b string) int) error {
	cName, err := libc.CString(name)
	if err != nil {
		return err
	}
	xCollations.mu.Lock()
	id := xCollations.ids.next()
	xCollations.m[id] = cmp
	xCollations.mu.Unlock()

	coll := &collation{
		zName: cName,
		pApp:  id,
		enc:   sqlite3.SQLITE_UTF8,
	}
	if err := c.createCollationInternal(coll); err != nil {
		libc.Xfree(c.tls, cName)
		xCollations.mu.Lock()
		delete(xCollations.m, id)
		xCollations.ids.reclaim(id)
		xCollations.mu.Unlock()
		return err
	}
	return nil
}

func (c *conn) registerScalarFunction(name string, nArg int32, pure bool, fn func(*FunctionContext, []driver.Value) (driver.Value, error)) error {
	cName, err := libc.CString(name)
	if err != nil {
		return err
	}
	enc := int32(sqlite3.SQLITE_UTF8)
	if pure {
		enc |= sqlite3.SQLITE_DETERMINISTIC
	}
	xFuncs.mu.Lock()
	id := xFuncs.ids.next()
	xFuncs.m[id] = fn
	xFuncs.mu.Unlock()

	udf := &userDefinedFunction{
		zFuncName: cName,
		nArg:      nArg,
		eTextRep:  enc,
		scalar:    true,
		pApp:      id,
	}
	if err := c.createFunctionInternal(udf); err != nil {
		libc.Xfree(c.tls, cName)
		xFuncs.mu.Lock()
		delete(xFuncs.m, id)
		xFuncs.ids.reclaim(id)
		xFuncs.mu.Unlock()
		return err
	}
	return nil
}

func (c *conn) registerAggregateFunction(name string, nArg int32, pure bool, factory func(FunctionContext) (AggregateFunction, error)) error {
	cName, err := libc.CString(name)
	if err != nil {
		return err
	}
	enc := int32(sqlite3.SQLITE_UTF8)
	if pure {
		enc |= sqlite3.SQLITE_DETERMINISTIC
	}
	xAggregateFactories.mu.Lock()
	id := xAggregateFactories.ids.next()
	xAggregateFactories.m[id] = factory
	xAggregateFactories.mu.Unlock()

	udf := &userDefinedFunction{
		zFuncName: cName,
		nArg:      nArg,
		eTextRep:  enc,
		scalar:    false,
		pApp:      id,
	}
	if err := c.createFunctionInternal(udf); err != nil {
		libc.Xfree(c.tls, cName)
		xAggregateFactories.mu.Lock()
		delete(xAggregateFactories.m, id)
		xAggregateFactories.ids.reclaim(id)
		xAggregateFactories.mu.Unlock()
		return err
	}
	return nil
}

// reflectScalar wraps a user function so it can be called from SQLite. It
// returns a driver.Value-shaped scalar function and the matching argument
// count for sqlite3_create_function (negative for variadic).
func reflectScalar(impl any) (func(*FunctionContext, []driver.Value) (driver.Value, error), int32, error) {
	v := reflect.ValueOf(impl)
	t := v.Type()
	if t.Kind() != reflect.Func {
		return nil, 0, fmt.Errorf("impl must be a function, got %T", impl)
	}

	nin := t.NumIn()
	variadic := t.IsVariadic()
	nArg := int32(nin)
	if variadic {
		nArg = -1
	}

	// Pre-validate argument types and pre-build converters.
	converters := make([]func(driver.Value) (reflect.Value, error), nin)
	for i := 0; i < nin; i++ {
		argT := t.In(i)
		if variadic && i == nin-1 {
			argT = argT.Elem()
		}
		conv, err := valueConverter(argT)
		if err != nil {
			return nil, 0, fmt.Errorf("arg %d: %w", i, err)
		}
		converters[i] = conv
	}

	// Validate return signature: must be (T) or (T, error).
	nout := t.NumOut()
	if nout < 1 || nout > 2 {
		return nil, 0, fmt.Errorf("impl must return (value) or (value, error), has %d returns", nout)
	}
	if nout == 2 && !t.Out(1).Implements(errType) {
		return nil, 0, fmt.Errorf("impl second return must be error, got %v", t.Out(1))
	}

	scalar := func(ctx *FunctionContext, args []driver.Value) (driver.Value, error) {
		var callArgs []reflect.Value
		if variadic {
			fixed := nin - 1
			if len(args) < fixed {
				return nil, fmt.Errorf("expected at least %d args, got %d", fixed, len(args))
			}
			callArgs = make([]reflect.Value, fixed, len(args))
			for i := 0; i < fixed; i++ {
				rv, err := converters[i](args[i])
				if err != nil {
					return nil, fmt.Errorf("arg %d: %w", i, err)
				}
				callArgs[i] = rv
			}
			varConv := converters[nin-1]
			for i := fixed; i < len(args); i++ {
				rv, err := varConv(args[i])
				if err != nil {
					return nil, fmt.Errorf("arg %d: %w", i, err)
				}
				callArgs = append(callArgs, rv)
			}
		} else {
			if len(args) != nin {
				return nil, fmt.Errorf("expected %d args, got %d", nin, len(args))
			}
			callArgs = make([]reflect.Value, nin)
			for i := 0; i < nin; i++ {
				rv, err := converters[i](args[i])
				if err != nil {
					return nil, fmt.Errorf("arg %d: %w", i, err)
				}
				callArgs[i] = rv
			}
		}
		out := v.Call(callArgs)
		if nout == 2 && !out[1].IsNil() {
			return nil, out[1].Interface().(error)
		}
		return goToDriverValue(out[0]), nil
	}

	return scalar, nArg, nil
}

// reflectAggregator validates that impl is `func() X` where X has a Step and
// Done method. It returns an AggregateFunction factory that wraps each instance.
func reflectAggregator(impl any) (func(FunctionContext) (AggregateFunction, error), int32, error) {
	v := reflect.ValueOf(impl)
	t := v.Type()
	if t.Kind() != reflect.Func {
		return nil, 0, fmt.Errorf("impl must be a function returning the aggregate state, got %T", impl)
	}
	if t.NumIn() != 0 || t.NumOut() != 1 {
		return nil, 0, fmt.Errorf("aggregate factory must be func() State")
	}
	stateT := t.Out(0)
	stateForCall := stateT
	if stateT.Kind() == reflect.Ptr { //nolint:govet // inline: reflect.Ptr alias kept for readability over the numeric constant
		stateForCall = stateT
	}

	stepM, ok := stateForCall.MethodByName("Step")
	if !ok {
		return nil, 0, fmt.Errorf("aggregate state type %v has no Step method", stateT)
	}
	doneM, ok := stateForCall.MethodByName("Done")
	if !ok {
		return nil, 0, fmt.Errorf("aggregate state type %v has no Done method", stateT)
	}

	// Step: func(state, args...) — receiver counts as first In.
	stepT := stepM.Type
	stepIn := stepT.NumIn() - 1 // exclude receiver
	stepVariadic := stepT.IsVariadic()
	nArg := int32(stepIn)
	if stepVariadic {
		nArg = -1
	}
	stepConv := make([]func(driver.Value) (reflect.Value, error), stepIn)
	for i := 0; i < stepIn; i++ {
		argT := stepT.In(i + 1) // skip receiver
		if stepVariadic && i == stepIn-1 {
			argT = argT.Elem()
		}
		conv, err := valueConverter(argT)
		if err != nil {
			return nil, 0, fmt.Errorf("Step arg %d: %w", i, err)
		}
		stepConv[i] = conv
	}

	// Done: must return (T) or (T, error).
	doneT := doneM.Type
	if doneT.NumIn() != 1 { // receiver only
		return nil, 0, fmt.Errorf("aggregate Done method must take no arguments")
	}
	doneOut := doneT.NumOut()
	if doneOut < 1 || doneOut > 2 {
		return nil, 0, fmt.Errorf("aggregate Done must return (value) or (value, error)")
	}
	if doneOut == 2 && !doneT.Out(1).Implements(errType) {
		return nil, 0, fmt.Errorf("aggregate Done second return must be error")
	}

	factory := func(_ FunctionContext) (AggregateFunction, error) {
		state := v.Call(nil)[0]
		return &reflectAggregate{
			state:        state,
			stepM:        stepM.Func,
			doneM:        doneM.Func,
			stepConv:     stepConv,
			stepVariadic: stepVariadic,
			stepFixed:    stepIn,
			doneOut:      doneOut,
		}, nil
	}
	return factory, nArg, nil
}

type reflectAggregate struct {
	state        reflect.Value
	stepM        reflect.Value
	doneM        reflect.Value
	stepConv     []func(driver.Value) (reflect.Value, error)
	stepVariadic bool
	stepFixed    int
	doneOut      int
}

func (r *reflectAggregate) Step(ctx *FunctionContext, rowArgs []driver.Value) error {
	args := make([]reflect.Value, 0, 1+len(rowArgs))
	args = append(args, r.state)
	if r.stepVariadic {
		fixed := r.stepFixed - 1
		if len(rowArgs) < fixed {
			return fmt.Errorf("expected at least %d Step args, got %d", fixed, len(rowArgs))
		}
		for i := 0; i < fixed; i++ {
			rv, err := r.stepConv[i](rowArgs[i])
			if err != nil {
				return fmt.Errorf("Step arg %d: %w", i, err)
			}
			args = append(args, rv)
		}
		varConv := r.stepConv[r.stepFixed-1]
		for i := fixed; i < len(rowArgs); i++ {
			rv, err := varConv(rowArgs[i])
			if err != nil {
				return fmt.Errorf("Step arg %d: %w", i, err)
			}
			args = append(args, rv)
		}
	} else {
		if len(rowArgs) != r.stepFixed {
			return fmt.Errorf("expected %d Step args, got %d", r.stepFixed, len(rowArgs))
		}
		for i, a := range rowArgs {
			rv, err := r.stepConv[i](a)
			if err != nil {
				return fmt.Errorf("Step arg %d: %w", i, err)
			}
			args = append(args, rv)
		}
	}
	r.stepM.Call(args)
	return nil
}

func (r *reflectAggregate) WindowInverse(ctx *FunctionContext, rowArgs []driver.Value) error {
	// Default reflective aggregates don't support window inverse; SQLite will
	// fall back to recomputation.
	return fmt.Errorf("aggregate does not support window inverse")
}

func (r *reflectAggregate) WindowValue(ctx *FunctionContext) (driver.Value, error) {
	out := r.doneM.Call([]reflect.Value{r.state})
	if r.doneOut == 2 && !out[1].IsNil() {
		return nil, out[1].Interface().(error)
	}
	return goToDriverValue(out[0]), nil
}

func (r *reflectAggregate) Final(ctx *FunctionContext) {
	// No-op: WindowValue is responsible for returning the result. SQLite calls
	// Final after WindowValue to release per-aggregate resources; reflective
	// aggregates have nothing to release.
}
