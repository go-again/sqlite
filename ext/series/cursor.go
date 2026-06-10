package series

import (
	"errors"

	sqlite "github.com/go-again/sqlite"
)

type seriesCursor struct {
	value int64
	start int64
	stop  int64
	step  int64
	desc  bool
	eof   bool
}

func (c *seriesCursor) Filter(idxNum int, _ string, args []sqlite.Value) error {
	plan := int64(idxNum)
	arg := func(shift uint) (int64, bool) {
		n := (plan >> shift) & 0xf
		if n == 0 || int(n-1) >= len(args) {
			return 0, false
		}
		return toInt64(args[n-1]), true
	}
	start, okStart := arg(0)
	stop, okStop := arg(4)
	step, okStep := arg(8)
	if !okStart || !okStop {
		return errors.New("generate_series: start and stop arguments are required")
	}
	if !okStep {
		step = 1
	}
	if step == 0 {
		return errors.New("generate_series: step must be non-zero")
	}

	c.start, c.stop, c.step = start, stop, step
	c.value = start
	c.desc = step < 0
	if c.desc {
		c.eof = start < stop
	} else {
		c.eof = start > stop
	}
	return nil
}

func (c *seriesCursor) Next() error {
	c.value += c.step
	if c.desc {
		c.eof = c.value < c.stop
	} else {
		c.eof = c.value > c.stop
	}
	return nil
}

func (c *seriesCursor) Eof() bool { return c.eof }

func (c *seriesCursor) Column(col int) (sqlite.Value, error) {
	switch col {
	case colStart:
		return c.start, nil
	case colStop:
		return c.stop, nil
	case colStep:
		return c.step, nil
	default: // colValue
		return c.value, nil
	}
}

func (c *seriesCursor) Rowid() (int64, error) { return c.value, nil }
func (c *seriesCursor) Close() error          { return nil }

// toInt64 coerces a value arriving from SQLite (INTEGER → int64, REAL →
// float64) to int64.
func toInt64(v sqlite.Value) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	default:
		return 0
	}
}
