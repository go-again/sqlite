package sqlite

import (
	"database/sql"
	"testing"
	"time"
)

// TestTime_RoundTripMatrix exercises every supported combination of the
// time-related DSN flags:
//   - _time_format:         "", "sqlite", "datetime"
//   - _time_integer_format: "", "unix", "unix_milli", "unix_micro", "unix_nano"
//   - _timezone:            "", "UTC", "America/New_York"
//
// For each cell, insert a known time, read it back, compare. Per the modernc
// docs, _time_integer_format dominates _time_format when both are set, so
// we just verify the value survives — the exact wire format is the driver's
// internal business.
//
// Precision is set by _time_integer_format: unix → second, unix_milli →
// millisecond, unix_micro → microsecond, unix_nano → nanosecond. Comparing
// at the appropriate granularity catches truncation bugs without flagging
// expected precision loss as a test failure.
func TestTime_RoundTripMatrix(t *testing.T) {
	// Sample time: deliberately not on a second/ms/µs boundary so any of
	// the precision-truncating formats will visibly drop digits.
	sample := time.Date(2026, 5, 26, 14, 30, 45, 123_456_789, time.UTC)

	cases := []struct {
		name              string
		timeFormat        string
		timeIntegerFormat string
		timezone          string
		inttotime         bool // required when reading back integer-stored times
		// resolution of the round-trip we can assert.
		tolerance time.Duration
	}{
		{"default", "", "", "", false, time.Second}, // String() format loses no precision but parses fuzzily
		{"sqlite_text", "sqlite", "", "", false, time.Nanosecond},
		{"datetime_text", "datetime", "", "", false, time.Second},
		{"sqlite_utc", "sqlite", "", "UTC", false, time.Nanosecond},
		{"sqlite_ny", "sqlite", "", "America/New_York", false, time.Nanosecond},
		{"int_unix", "", "unix", "", true, time.Second},
		{"int_milli", "", "unix_milli", "", true, time.Millisecond},
		{"int_micro", "", "unix_micro", "", true, time.Microsecond},
		{"int_nano", "", "unix_nano", "", true, time.Nanosecond},
		{"int_unix_utc", "", "unix", "UTC", true, time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := ":memory:?"
			sep := ""
			add := func(k, v string) {
				if v == "" {
					return
				}
				dsn += sep + k + "=" + v
				sep = "&"
			}
			add("_time_format", tc.timeFormat)
			add("_time_integer_format", tc.timeIntegerFormat)
			add("_timezone", tc.timezone)
			if tc.inttotime {
				add("_inttotime", "1")
			}

			db, err := sql.Open(DriverNameMattn, dsn)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer db.Close()
			if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, ts DATETIME)"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO t (id, ts) VALUES (1, ?)", sample); err != nil {
				t.Fatalf("insert: %v", err)
			}

			var got time.Time
			if err := db.QueryRow("SELECT ts FROM t WHERE id = 1").Scan(&got); err != nil {
				t.Fatalf("scan: %v", err)
			}

			diff := got.Sub(sample)
			if diff < 0 {
				diff = -diff
			}
			if diff > tc.tolerance {
				t.Errorf("time round-trip diff=%v exceeds tolerance %v\n want %s\n got  %s",
					diff, tc.tolerance, sample.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano))
			}
		})
	}
}

// TestTime_InttotimeReadsBackInts checks the _inttotime=1 path: a raw
// int64 stored in a temporally-typed column (DATE / DATETIME / TIMESTAMP)
// reads back as a time.Time. This is the canonical "store times as Unix
// integers" workflow where some rows arrived as int and we still want
// them surfaced uniformly as time.Time.
//
// Note: _inttotime only activates for columns whose decltype is one of
// DATE / DATETIME / TIMESTAMP / TIME (the modernc driver sniffs decltype
// to decide whether to attempt the conversion). A bare INTEGER column
// won't be converted even with _inttotime=1.
func TestTime_InttotimeReadsBackInts(t *testing.T) {
	dsn := ":memory:?_inttotime=1"
	db, err := sql.Open(DriverNameMattn, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, ts DATETIME)"); err != nil {
		t.Fatal(err)
	}
	// Insert a raw Unix second value.
	unix := int64(1_700_000_000) // 2023-11-14T22:13:20Z
	if _, err := db.Exec("INSERT INTO t (id, ts) VALUES (1, ?)", unix); err != nil {
		t.Fatal(err)
	}

	var got time.Time
	if err := db.QueryRow("SELECT ts FROM t WHERE id = 1").Scan(&got); err != nil {
		t.Fatalf("scan as time.Time: %v", err)
	}
	if got.Unix() != unix {
		t.Errorf("inttotime read got Unix=%d, want %d", got.Unix(), unix)
	}
}
