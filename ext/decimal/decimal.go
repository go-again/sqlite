// Package decimal adds exact base-10 arithmetic scalar functions, for
// workloads where binary floating point's rounding is unacceptable
// (money, tax, billing). Values are decimal strings computed through
// math/big.Rat, so add / sub / mul / compare are exact; division renders
// to a fixed number of places.
//
//	decimal(x)            -- normalize to canonical decimal text
//	decimal_add(a, b)     -- a + b      (exact)
//	decimal_sub(a, b)     -- a - b      (exact)
//	decimal_mul(a, b)     -- a * b      (exact)
//	decimal_div(a, b)     -- a / b      (to 30 places)
//	decimal_cmp(a, b)     -- -1 / 0 / 1
//	decimal_neg(x)        -- -x
//	decimal_abs(x)        -- |x|
//	decimal_round(x, n)   -- round to n fractional digits
//	decimal_floor(x)      -- greatest integer <= x
//	decimal_ceil(x)       -- least integer >= x
//	decimal_sum(x)        -- exact aggregate sum
//
// Inputs may be TEXT, INTEGER, or REAL; a REAL is read as the shortest
// decimal that round-trips it (so decimal_add(0.1, 0.2) is exactly 0.3,
// not 0.30000000000000004). For full exactness, store decimals as TEXT.
// Any NULL argument yields NULL.
package decimal

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/internal/bigdec"
)

// Register installs the decimal_* functions on c.
//
// Per-connection registration. For pool-wide install, blank-import the
// auto sub-package:
//
//	import _ "github.com/go-again/sqlite/ext/decimal/auto"
func Register(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc("decimal", normalize, true),
		c.RegisterFunc("decimal_add", add, true),
		c.RegisterFunc("decimal_sub", sub, true),
		c.RegisterFunc("decimal_mul", mul, true),
		c.RegisterFunc("decimal_div", div, true),
		c.RegisterFunc("decimal_cmp", cmp, true),
		c.RegisterFunc("decimal_neg", neg, true),
		c.RegisterFunc("decimal_abs", abs, true),
		c.RegisterFunc("decimal_round", round, true),
		c.RegisterFunc("decimal_floor", floor, true),
		c.RegisterFunc("decimal_ceil", ceil, true),
		c.RegisterAggregator("decimal_sum", newSum, true),
	)
}

// toRat coerces a SQL value to a Rat. The bool is false for SQL NULL so
// callers can propagate NULL.
func toRat(v any) (*big.Rat, bool, error) {
	switch x := v.(type) {
	case nil:
		return nil, false, nil
	case string:
		r, err := bigdec.Parse(x)
		return r, err == nil, err
	case []byte:
		r, err := bigdec.Parse(string(x))
		return r, err == nil, err
	case int64:
		return new(big.Rat).SetInt64(x), true, nil
	case float64:
		// Read the REAL as the shortest decimal that round-trips it, not
		// its raw binary value — what the user most likely typed.
		r, err := bigdec.Parse(strconv.FormatFloat(x, 'g', -1, 64))
		return r, err == nil, err
	default:
		return nil, false, fmt.Errorf("decimal: unsupported argument type %T", v)
	}
}

// binary parses two operands, short-circuiting to (nil, false) when
// either is NULL.
func binary(a, b any) (ra, rb *big.Rat, ok bool, err error) {
	ra, oka, err := toRat(a)
	if err != nil {
		return nil, nil, false, err
	}
	rb, okb, err := toRat(b)
	if err != nil {
		return nil, nil, false, err
	}
	return ra, rb, oka && okb, nil
}

func normalize(x any) (any, error) {
	r, ok, err := toRat(x)
	if err != nil || !ok {
		return nil, err
	}
	return bigdec.String(r), nil
}

func add(a, b any) (any, error) {
	ra, rb, ok, err := binary(a, b)
	if err != nil || !ok {
		return nil, err
	}
	return bigdec.String(new(big.Rat).Add(ra, rb)), nil
}

func sub(a, b any) (any, error) {
	ra, rb, ok, err := binary(a, b)
	if err != nil || !ok {
		return nil, err
	}
	return bigdec.String(new(big.Rat).Sub(ra, rb)), nil
}

func mul(a, b any) (any, error) {
	ra, rb, ok, err := binary(a, b)
	if err != nil || !ok {
		return nil, err
	}
	return bigdec.String(new(big.Rat).Mul(ra, rb)), nil
}

func div(a, b any) (any, error) {
	ra, rb, ok, err := binary(a, b)
	if err != nil || !ok {
		return nil, err
	}
	if rb.Sign() == 0 {
		return nil, errors.New("decimal_div: division by zero")
	}
	return bigdec.String(new(big.Rat).Quo(ra, rb)), nil
}

func cmp(a, b any) (any, error) {
	ra, rb, ok, err := binary(a, b)
	if err != nil || !ok {
		return nil, err
	}
	return int64(ra.Cmp(rb)), nil
}

func neg(x any) (any, error) {
	r, ok, err := toRat(x)
	if err != nil || !ok {
		return nil, err
	}
	return bigdec.String(new(big.Rat).Neg(r)), nil
}

func abs(x any) (any, error) {
	r, ok, err := toRat(x)
	if err != nil || !ok {
		return nil, err
	}
	return bigdec.String(new(big.Rat).Abs(r)), nil
}

func round(x any, n int64) (any, error) {
	r, ok, err := toRat(x)
	if err != nil || !ok {
		return nil, err
	}
	return bigdec.Round(r, int(n)), nil
}

func floor(x any) (any, error) {
	r, ok, err := toRat(x)
	if err != nil || !ok {
		return nil, err
	}
	return bigdec.Floor(r), nil
}

func ceil(x any) (any, error) {
	r, ok, err := toRat(x)
	if err != nil || !ok {
		return nil, err
	}
	return bigdec.Ceil(r), nil
}

// sum is the decimal_sum aggregate state. One instance per aggregation;
// NULL rows are skipped, matching SQL's sum() semantics.
type sum struct{ acc *big.Rat }

func newSum() *sum { return &sum{acc: new(big.Rat)} }

func (s *sum) Step(x any) error {
	r, ok, err := toRat(x)
	if err != nil || !ok {
		return err // NULL skipped (ok==false, err==nil)
	}
	s.acc.Add(s.acc, r)
	return nil
}

func (s *sum) Done() string { return bigdec.String(s.acc) }
