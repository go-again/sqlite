// Package money adds fixed-point currency scalar functions over the
// same exact base-10 backend as ext/decimal. Every result is rounded to
// two fractional digits, the granularity money is actually stored at, so
// chains of additions and multiplications don't drift the way binary
// floats do.
//
//	money(x)              -- round x to 2 dp (canonical money text)
//	money_add(a, b)       -- round(a + b, 2)
//	money_sub(a, b)       -- round(a - b, 2)
//	money_mul(a, b)       -- round(a * b, 2)   (e.g. price × quantity)
//	money_format(x[, s])  -- "$1,234.56" with thousands separators
//
// Inputs may be TEXT / INTEGER / REAL (a REAL is read as the shortest
// decimal that round-trips it). Any NULL argument yields NULL.
package money

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	sqlite "gosqlite.org"
	"gosqlite.org/ext/internal/bigdec"
)

const scale = 2 // money is stored to the cent

// Register installs the money_* functions on c.
//
// Per-connection registration. For pool-wide install, blank-import the
// auto sub-package:
//
//	import _ "gosqlite.org/ext/money/auto"
func Register(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc("money", normalize, true),
		c.RegisterFunc("money_add", add, true),
		c.RegisterFunc("money_sub", sub, true),
		c.RegisterFunc("money_mul", mul, true),
		c.RegisterFunc("money_format", format, true),
	)
}

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
		r, err := bigdec.Parse(strconv.FormatFloat(x, 'g', -1, 64))
		return r, err == nil, err
	default:
		return nil, false, fmt.Errorf("money: unsupported argument type %T", v)
	}
}

// round2 renders r to exactly two fractional digits.
func round2(r *big.Rat) string { return bigdec.Round(r, scale) }

func normalize(x any) (any, error) {
	r, ok, err := toRat(x)
	if err != nil || !ok {
		return nil, err
	}
	return round2(r), nil
}

func add(a, b any) (any, error) { return combine(a, b, (*big.Rat).Add) }
func sub(a, b any) (any, error) { return combine(a, b, (*big.Rat).Sub) }
func mul(a, b any) (any, error) { return combine(a, b, (*big.Rat).Mul) }

func combine(a, b any, op func(z, x, y *big.Rat) *big.Rat) (any, error) {
	ra, oka, err := toRat(a)
	if err != nil {
		return nil, err
	}
	rb, okb, err := toRat(b)
	if err != nil {
		return nil, err
	}
	if !oka || !okb {
		return nil, nil
	}
	return round2(op(new(big.Rat), ra, rb)), nil
}

func format(x any, symbol ...string) (any, error) {
	r, ok, err := toRat(x)
	if err != nil || !ok {
		return nil, err
	}
	sym := "$"
	if len(symbol) > 0 {
		sym = symbol[0]
	}
	s := round2(r) // e.g. "-1234.56"
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, fracPart, _ := strings.Cut(s, ".")
	grouped := groupThousands(intPart)
	out := sym + grouped + "." + fracPart
	if neg {
		out = "-" + out
	}
	return out, nil
}

// groupThousands inserts commas every three digits from the right.
func groupThousands(digits string) string {
	n := len(digits)
	if n <= 3 {
		return digits
	}
	var b strings.Builder
	lead := n % 3
	if lead > 0 {
		b.WriteString(digits[:lead])
		if n > lead {
			b.WriteByte(',')
		}
	}
	for i := lead; i < n; i += 3 {
		b.WriteString(digits[i : i+3])
		if i+3 < n {
			b.WriteByte(',')
		}
	}
	return b.String()
}
