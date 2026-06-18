package timeext_test

import (
	"context"
	"database/sql"
	"testing"

	timeext "github.com/go-again/sqlite/ext/time"
	"github.com/go-again/sqlite/internal/testhelp"
)

func openDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	testhelp.WithConnectHook(t, timeext.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return context.Background(), db
}

func TestTime_UnixRoundTrip(t *testing.T) {
	ctx, db := openDB(t)
	var secs int64
	if err := db.QueryRowContext(ctx, `SELECT time_unix('2021-01-01T00:00:00Z')`).Scan(&secs); err != nil {
		t.Fatal(err)
	}
	if secs != 1609459200 {
		t.Errorf("time_unix = %d, want 1609459200", secs)
	}
	var ts string
	if err := db.QueryRowContext(ctx, `SELECT time_from_unix(1609459200)`).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if ts != "2021-01-01T00:00:00Z" {
		t.Errorf("time_from_unix = %q, want 2021-01-01T00:00:00Z", ts)
	}
}

func TestTime_AddAndDiff(t *testing.T) {
	ctx, db := openDB(t)
	var got string
	if err := db.QueryRowContext(ctx, `SELECT time_add('2021-01-01 00:00:00', '24h')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "2021-01-02T00:00:00Z" {
		t.Errorf("time_add = %q, want 2021-01-02T00:00:00Z", got)
	}
	var sec float64
	if err := db.QueryRowContext(ctx,
		`SELECT time_diff('2021-01-01T01:00:00Z','2021-01-01T00:00:00Z')`).Scan(&sec); err != nil {
		t.Fatal(err)
	}
	if sec != 3600 {
		t.Errorf("time_diff = %v, want 3600", sec)
	}
}

func TestTime_Part(t *testing.T) {
	ctx, db := openDB(t)
	cases := []struct {
		field string
		want  int64
	}{
		{"year", 2021}, {"month", 3}, {"day", 14},
		{"hour", 15}, {"minute", 9}, {"second", 26},
	}
	for _, c := range cases {
		var got int64
		if err := db.QueryRowContext(ctx,
			`SELECT time_part('2021-03-14T15:09:26Z', ?)`, c.field).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("time_part(%s) = %d, want %d", c.field, got, c.want)
		}
	}
}

func TestTime_Trunc(t *testing.T) {
	ctx, db := openDB(t)
	var got string
	if err := db.QueryRowContext(ctx, `SELECT time_trunc('2021-03-14T15:09:26Z', '1h')`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "2021-03-14T15:00:00Z" {
		t.Errorf("time_trunc = %q, want 2021-03-14T15:00:00Z", got)
	}
}

func TestTime_NullPropagates(t *testing.T) {
	ctx, db := openDB(t)
	var v any
	if err := db.QueryRowContext(ctx, `SELECT time_add(NULL, '1h')`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("time_add(NULL,…) = %v, want NULL", v)
	}
}

func TestTime_Now(t *testing.T) {
	ctx, db := openDB(t)
	var a string
	if err := db.QueryRowContext(ctx, `SELECT time_now()`).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if len(a) < 20 { // RFC3339 nanos
		t.Errorf("time_now() = %q, not a timestamp", a)
	}
}
