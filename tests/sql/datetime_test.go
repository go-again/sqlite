package sql_test

import (
	"strings"
	"testing"
)

func TestDateTime_BasicFormat(t *testing.T) {
	db := openDB(t)
	cases := []struct {
		sql, want string
	}{
		{`select date('2026-05-26')`, "2026-05-26"},
		{`select time('15:04:05')`, "15:04:05"},
		{`select datetime('2026-05-26 15:04:05')`, "2026-05-26 15:04:05"},
		{`select date('2026-05-26 23:59:59')`, "2026-05-26"},
		{`select time('2026-05-26T15:04:05')`, "15:04:05"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			var got string
			scanOne(t, db, &got, c.sql)
			if got != c.want {
				t.Errorf("%s = %q, want %q", c.sql, got, c.want)
			}
		})
	}
}

func TestDateTime_Julianday(t *testing.T) {
	db := openDB(t)
	var jd float64
	scanOne(t, db, &jd, `select julianday('2000-01-01 12:00:00')`)
	// J2000 epoch is JD 2451545.0.
	if jd < 2451544 || jd > 2451546 {
		t.Errorf("julianday(J2000)=%f, want ~2451545", jd)
	}
}

func TestDateTime_Unixepoch(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.38" {
		t.Skipf("unixepoch requires SQLite >= 3.38, have %s", v)
	}
	var ts int64
	scanOne(t, db, &ts, `select unixepoch('1970-01-01 00:00:00')`)
	if ts != 0 {
		t.Errorf("unixepoch(epoch)=%d, want 0", ts)
	}
}

func TestDateTime_StrftimeFormats(t *testing.T) {
	db := openDB(t)
	cases := []struct {
		sql, want string
	}{
		{`select strftime('%Y', '2026-05-26')`, "2026"},
		{`select strftime('%m', '2026-05-26')`, "05"},
		{`select strftime('%d', '2026-05-26')`, "26"},
		{`select strftime('%H:%M:%S', '12:34:56')`, "12:34:56"},
		{`select strftime('%w', '2026-05-26')`, "2"}, // Tuesday = 2
		{`select strftime('%j', '2026-12-31')`, "365"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			var got string
			scanOne(t, db, &got, c.sql)
			if got != c.want {
				t.Errorf("%s = %q, want %q", c.sql, got, c.want)
			}
		})
	}
}

func TestDateTime_Modifiers(t *testing.T) {
	db := openDB(t)
	cases := []struct {
		sql, want string
	}{
		{`select date('2026-05-26', 'start of year')`, "2026-01-01"},
		{`select date('2026-05-26', 'start of month')`, "2026-05-01"},
		{`select date('2026-05-26', 'start of day')`, "2026-05-26"},
		{`select date('2026-05-26', '+1 day')`, "2026-05-27"},
		{`select date('2026-05-26', '-7 days')`, "2026-05-19"},
		{`select date('2026-05-26', '+1 month')`, "2026-06-26"},
		{`select date('2026-05-26', '+1 year')`, "2027-05-26"},
		{`select datetime('2026-05-26 12:00:00', '+1 hour')`, "2026-05-26 13:00:00"},
		{`select datetime('2026-05-26 12:00:00', '+30 minutes')`, "2026-05-26 12:30:00"},
		{`select date('2026-05-26', 'weekday 0')`, "2026-05-31"}, // next Sunday
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			var got string
			scanOne(t, db, &got, c.sql)
			if got != c.want {
				t.Errorf("%s = %q, want %q", c.sql, got, c.want)
			}
		})
	}
}

func TestDateTime_NowKeyword(t *testing.T) {
	db := openDB(t)
	// 'now' should resolve to a parseable datetime string.
	var got string
	scanOne(t, db, &got, `select datetime('now')`)
	if !strings.Contains(got, "-") || !strings.Contains(got, ":") {
		t.Errorf("datetime('now')=%q, want YYYY-MM-DD HH:MM:SS form", got)
	}
}

func TestDateTime_UtcLocaltime(t *testing.T) {
	db := openDB(t)
	// 'utc' and 'localtime' modifiers transform a datetime; just confirm
	// they parse and return a valid form. Don't pin specific TZ offsets
	// because they depend on the test host.
	var got string
	scanOne(t, db, &got, `select datetime('2026-05-26 12:00:00', 'utc')`)
	if got == "" {
		t.Errorf("datetime(utc) empty")
	}
	scanOne(t, db, &got, `select datetime('2026-05-26 12:00:00', 'localtime')`)
	if got == "" {
		t.Errorf("datetime(localtime) empty")
	}
}

func TestDateTime_SubsecondPrecision(t *testing.T) {
	db := openDB(t)
	if v := sqliteVersion(t, db); v < "3.42" {
		t.Skipf("subsecond modifier requires SQLite >= 3.42, have %s", v)
	}
	var got string
	scanOne(t, db, &got, `select strftime('%Y-%m-%dT%H:%M:%f', '2026-05-26 12:00:00.123')`)
	if !strings.Contains(got, "12:00:00.123") {
		t.Errorf("subsecond=%q, want contains 12:00:00.123", got)
	}
}
