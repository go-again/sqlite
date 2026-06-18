// Package bigdec is the exact base-10 arithmetic backend shared by
// ext/decimal and ext/money. Values are carried as decimal strings and
// computed through math/big.Rat, so add / sub / mul / compare are exact;
// division (and any value whose reduced denominator has a prime factor
// other than 2 or 5) is rendered to a fixed number of places.
package bigdec

import (
	"errors"
	"math/big"
	"strings"
)

// DivScale is the number of fractional digits used when a result is not
// a terminating decimal (the typical case for division).
const DivScale = 30

// Parse converts a decimal string ("12.34", "-0.5", "100") to a Rat.
// Surrounding spaces are tolerated; empty input is an error.
func Parse(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("bigdec: empty number")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, errors.New("bigdec: invalid number: " + s)
	}
	return r, nil
}

// String renders r as its shortest exact decimal when terminating,
// otherwise to DivScale places. Trailing zeros are trimmed.
func String(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}
	den := new(big.Int).Set(r.Denom())
	a := removeFactor(den, 2)
	b := removeFactor(den, 5)
	if den.Cmp(oneInt) == 0 { // denominator was 2^a·5^b → terminating
		return trimZeros(r.FloatString(max(a, b)))
	}
	return trimZeros(r.FloatString(DivScale))
}

// Round renders r rounded (half away from zero, big.Rat's rule) to
// exactly n fractional digits. A negative n is treated as 0.
func Round(r *big.Rat, n int) string {
	if n < 0 {
		n = 0
	}
	return r.FloatString(n)
}

// Floor returns the greatest integer <= r as a decimal string.
func Floor(r *big.Rat) string {
	q := new(big.Int).Quo(r.Num(), r.Denom())
	if r.Sign() < 0 && !r.IsInt() {
		q.Sub(q, oneInt)
	}
	return q.String()
}

// Ceil returns the least integer >= r as a decimal string.
func Ceil(r *big.Rat) string {
	q := new(big.Int).Quo(r.Num(), r.Denom())
	if r.Sign() > 0 && !r.IsInt() {
		q.Add(q, oneInt)
	}
	return q.String()
}

var oneInt = big.NewInt(1)

// removeFactor divides f out of n in place and returns how many times it
// divided evenly.
func removeFactor(n *big.Int, f int64) int {
	bf := big.NewInt(f)
	count := 0
	q, m := new(big.Int), new(big.Int)
	for n.Cmp(oneInt) != 0 {
		q.QuoRem(n, bf, m)
		if m.Sign() != 0 {
			break
		}
		n.Set(q)
		count++
	}
	return count
}

// trimZeros drops trailing zeros (and a trailing dot) from a fixed-point
// decimal string. "0.300" → "0.3", "5.00" → "5".
func trimZeros(s string) string {
	if !strings.ContainsRune(s, '.') {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}
