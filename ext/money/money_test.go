package money_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/go-again/sqlite/ext/money"
	"github.com/go-again/sqlite/internal/testhelp"
)

func openDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	testhelp.WithConnectHook(t, money.Register)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return context.Background(), db
}

func str(t *testing.T, ctx context.Context, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var s string
	if err := db.QueryRowContext(ctx, q, args...).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return s
}

func TestMoney_FixedPoint(t *testing.T) {
	ctx, db := openDB(t)
	cases := []struct{ q, want string }{
		{`SELECT money('1.5')`, "1.50"},
		{`SELECT money('1')`, "1.00"},
		{`SELECT money_add('1.11','2.22')`, "3.33"},
		{`SELECT money_sub('10.00','0.01')`, "9.99"},
		{`SELECT money_mul('10','0.1')`, "1.00"},
		{`SELECT money_mul('19.99','3')`, "59.97"},
	}
	for _, c := range cases {
		if got := str(t, ctx, db, c.q); got != c.want {
			t.Errorf("%s = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestMoney_Format(t *testing.T) {
	ctx, db := openDB(t)
	cases := []struct{ q, want string }{
		{`SELECT money_format('1234.5')`, "$1,234.50"},
		{`SELECT money_format('999.99')`, "$999.99"},
		{`SELECT money_format('1234567.89')`, "$1,234,567.89"},
		{`SELECT money_format('-1234.56')`, "-$1,234.56"},
		{`SELECT money_format('1234.5', '€')`, "€1,234.50"},
		{`SELECT money_format('0')`, "$0.00"},
	}
	for _, c := range cases {
		if got := str(t, ctx, db, c.q); got != c.want {
			t.Errorf("%s = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestMoney_NullPropagates(t *testing.T) {
	ctx, db := openDB(t)
	var v any
	if err := db.QueryRowContext(ctx, `SELECT money_add(NULL, '1.00')`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Errorf("money_add(NULL,…) = %v, want NULL", v)
	}
}
