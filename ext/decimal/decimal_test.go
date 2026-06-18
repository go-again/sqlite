package decimal_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/go-again/sqlite/ext/decimal"
	"github.com/go-again/sqlite/internal/testhelp"
)

func openDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	testhelp.WithConnectHook(t, decimal.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return context.Background(), db
}

func scalar[T any](t *testing.T, ctx context.Context, db *sql.DB, q string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRowContext(ctx, q, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return v
}

func TestDecimal_ExactArithmetic(t *testing.T) {
	ctx, db := openDB(t)
	cases := []struct{ q, want string }{
		{`SELECT decimal_add('0.1','0.2')`, "0.3"},
		{`SELECT decimal_sub('0.3','0.1')`, "0.2"},
		{`SELECT decimal_mul('0.1','0.1')`, "0.01"},
		{`SELECT decimal_mul('1.5','4')`, "6"},
		{`SELECT decimal('100.500')`, "100.5"},
		{`SELECT decimal_neg('5')`, "-5"},
		{`SELECT decimal_abs('-7.25')`, "7.25"},
		{`SELECT decimal_round('2.567', 2)`, "2.57"},
		{`SELECT decimal_round('2.5', 0)`, "3"},
		{`SELECT decimal_floor('-1.5')`, "-2"},
		{`SELECT decimal_ceil('-1.5')`, "-1"},
		{`SELECT decimal_floor('1.9')`, "1"},
		{`SELECT decimal_ceil('1.1')`, "2"},
	}
	for _, c := range cases {
		if got := scalar[string](t, ctx, db, c.q); got != c.want {
			t.Errorf("%s = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestDecimal_DivisionPrecision(t *testing.T) {
	ctx, db := openDB(t)
	got := scalar[string](t, ctx, db, `SELECT decimal_div('1','3')`)
	if len(got) < 10 || got[:4] != "0.33" {
		t.Errorf("decimal_div('1','3') = %q, want ~0.333…", got)
	}
	if got := scalar[string](t, ctx, db, `SELECT decimal_div('10','4')`); got != "2.5" {
		t.Errorf("decimal_div('10','4') = %q, want 2.5", got)
	}
	var derr string
	if err := db.QueryRowContext(ctx, `SELECT decimal_div('1','0')`).Scan(&derr); err == nil {
		t.Error("decimal_div by zero did not error")
	}
}

func TestDecimal_Cmp(t *testing.T) {
	ctx, db := openDB(t)
	for _, c := range []struct {
		q    string
		want int64
	}{
		{`SELECT decimal_cmp('1.10','1.1')`, 0},
		{`SELECT decimal_cmp('2','10')`, -1},
		{`SELECT decimal_cmp('0.3','0.29')`, 1},
	} {
		if got := scalar[int64](t, ctx, db, c.q); got != c.want {
			t.Errorf("%s = %d, want %d", c.q, got, c.want)
		}
	}
}

func TestDecimal_RealInputRoundTrips(t *testing.T) {
	ctx, db := openDB(t)
	// REAL literals are read as the decimal the user typed, so binary
	// float noise does not leak in.
	if got := scalar[string](t, ctx, db, `SELECT decimal_add(0.1, 0.2)`); got != "0.3" {
		t.Errorf("decimal_add(0.1,0.2) = %q, want 0.3", got)
	}
}

func TestDecimal_NullPropagates(t *testing.T) {
	ctx, db := openDB(t)
	var v any
	if err := db.QueryRowContext(ctx, `SELECT decimal_add('1', NULL)`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("decimal_add('1', NULL) = %v, want NULL", v)
	}
}

func TestDecimal_SumAggregate(t *testing.T) {
	ctx, db := openDB(t)
	if _, err := db.ExecContext(ctx, `CREATE TABLE t(v TEXT); INSERT INTO t VALUES ('0.1'),('0.2'),('0.3'),(NULL),('100')`); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, ctx, db, `SELECT decimal_sum(v) FROM t`); got != "100.6" {
		t.Errorf("decimal_sum = %q, want 100.6", got)
	}
}
