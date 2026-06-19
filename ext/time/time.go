// Package timeext adds Go-time-backed scalar functions that go beyond
// SQLite's built-in datetime(): duration arithmetic with Go duration
// syntax, field extraction, truncation, and unix conversions.
//
//	time_now()              -- current instant, RFC3339 nanos, UTC (non-deterministic)
//	time_unix(t)            -- t as unix seconds (INTEGER)
//	time_from_unix(secs)    -- unix seconds → RFC3339 UTC
//	time_add(t, dur)        -- t + a Go duration ('24h', '-90m', '1h30m')
//	time_diff(a, b)         -- a - b, in seconds (REAL)
//	time_part(t, field)     -- year|month|day|hour|minute|second|nanosecond|weekday|yearday
//	time_trunc(t, dur)      -- round t down to a multiple of dur (since the epoch, UTC)
//	time_format(t, layout)  -- reformat using a Go reference layout
//
// A timestamp argument may be RFC3339(/nano), 'YYYY-MM-DD HH:MM:SS', or
// 'YYYY-MM-DD'. Any NULL argument yields NULL. The package name is
// timeext to avoid shadowing the standard library; the SQL functions are
// the time_* names above.
package timeext

import (
	"errors"
	"fmt"
	"time"

	sqlite "gosqlite.org"
)

// Register installs the time_* functions on c.
//
// Per-connection registration. For pool-wide install, blank-import the
// auto sub-package:
//
//	import _ "gosqlite.org/ext/time/auto"
func Register(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc("time_now", now, false), // wall-clock: not deterministic
		c.RegisterFunc("time_unix", unixOf, true),
		c.RegisterFunc("time_from_unix", fromUnix, true),
		c.RegisterFunc("time_add", addDur, true),
		c.RegisterFunc("time_diff", diff, true),
		c.RegisterFunc("time_part", part, true),
		c.RegisterFunc("time_trunc", trunc, true),
		c.RegisterFunc("time_format", format, true),
	)
}

var layouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parse coerces a SQL value to a time. The bool is false for NULL.
func parse(v any) (time.Time, bool, error) {
	switch x := v.(type) {
	case nil:
		return time.Time{}, false, nil
	case int64: // bare integer is read as unix seconds
		return time.Unix(x, 0).UTC(), true, nil
	case float64:
		return time.Unix(int64(x), 0).UTC(), true, nil
	case []byte:
		return parseString(string(x))
	case string:
		return parseString(x)
	default:
		return time.Time{}, false, fmt.Errorf("time: unsupported argument type %T", v)
	}
}

func parseString(s string) (time.Time, bool, error) {
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("time: cannot parse %q", s)
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func unixOf(v any) (any, error) {
	t, ok, err := parse(v)
	if err != nil || !ok {
		return nil, err
	}
	return t.Unix(), nil
}

func fromUnix(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int64:
		return time.Unix(x, 0).UTC().Format(time.RFC3339), nil
	case float64:
		return time.Unix(int64(x), 0).UTC().Format(time.RFC3339), nil
	default:
		return nil, fmt.Errorf("time_from_unix: want INTEGER seconds, got %T", v)
	}
}

func addDur(v any, dur string) (any, error) {
	t, ok, err := parse(v)
	if err != nil || !ok {
		return nil, err
	}
	d, err := time.ParseDuration(dur)
	if err != nil {
		return nil, fmt.Errorf("time_add: %w", err)
	}
	return t.Add(d).Format(time.RFC3339Nano), nil
}

func diff(a, b any) (any, error) {
	ta, oka, err := parse(a)
	if err != nil {
		return nil, err
	}
	tb, okb, err := parse(b)
	if err != nil {
		return nil, err
	}
	if !oka || !okb {
		return nil, nil
	}
	return ta.Sub(tb).Seconds(), nil
}

func part(v any, field string) (any, error) {
	t, ok, err := parse(v)
	if err != nil || !ok {
		return nil, err
	}
	switch field {
	case "year":
		return int64(t.Year()), nil
	case "month":
		return int64(t.Month()), nil
	case "day":
		return int64(t.Day()), nil
	case "hour":
		return int64(t.Hour()), nil
	case "minute":
		return int64(t.Minute()), nil
	case "second":
		return int64(t.Second()), nil
	case "nanosecond":
		return int64(t.Nanosecond()), nil
	case "weekday":
		return int64(t.Weekday()), nil // 0=Sunday
	case "yearday":
		return int64(t.YearDay()), nil
	default:
		return nil, fmt.Errorf("time_part: unknown field %q", field)
	}
}

func trunc(v any, dur string) (any, error) {
	t, ok, err := parse(v)
	if err != nil || !ok {
		return nil, err
	}
	d, err := time.ParseDuration(dur)
	if err != nil {
		return nil, fmt.Errorf("time_trunc: %w", err)
	}
	if d <= 0 {
		return nil, errors.New("time_trunc: duration must be positive")
	}
	return t.Truncate(d).Format(time.RFC3339Nano), nil
}

func format(v any, layout string) (any, error) {
	t, ok, err := parse(v)
	if err != nil || !ok {
		return nil, err
	}
	return t.Format(layout), nil
}
