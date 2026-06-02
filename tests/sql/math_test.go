package sql_test

import (
	"database/sql"
	"math"
	"testing"
)

// hasMathFunctions probes whether the bundled SQLite has the optional
// math functions compiled in (the SQLITE_ENABLE_MATH_FUNCTIONS flag).
// modernc/sqlite ships them on by default at the pin in `go.mod`; this
// guard is for forward-compat in case future bumps split them out.
func hasMathFunctions(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var v float64
	err := db.QueryRow(`select sqrt(4.0)`).Scan(&v)
	return err == nil
}

func TestMath_Abs(t *testing.T) {
	db := openDB(t)
	cases := []struct {
		sql  string
		want float64
	}{
		{`select abs(-5)`, 5},
		{`select abs(5)`, 5},
		{`select abs(-3.14)`, 3.14},
		{`select abs(0)`, 0},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			var got float64
			scanOne(t, db, &got, c.sql)
			if math.Abs(got-c.want) > 1e-9 {
				t.Errorf("%s = %f, want %f", c.sql, got, c.want)
			}
		})
	}
}

func TestMath_MaxMinScalar(t *testing.T) {
	db := openDB(t)
	var got int64
	scanOne(t, db, &got, `select max(1, 5, 3)`)
	if got != 5 {
		t.Errorf("max=%d, want 5", got)
	}
	scanOne(t, db, &got, `select min(1, 5, 3)`)
	if got != 1 {
		t.Errorf("min=%d, want 1", got)
	}
}

func TestMath_Round(t *testing.T) {
	db := openDB(t)
	cases := []struct {
		sql  string
		want float64
	}{
		{`select round(1.5)`, 2},
		{`select round(2.5)`, 3},
		{`select round(-1.5)`, -2},
		{`select round(1.567, 2)`, 1.57},
		{`select round(1.234, 0)`, 1},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			var got float64
			scanOne(t, db, &got, c.sql)
			if math.Abs(got-c.want) > 1e-9 {
				t.Errorf("%s = %f, want %f", c.sql, got, c.want)
			}
		})
	}
}

func TestMath_RandomBlob(t *testing.T) {
	db := openDB(t)
	var bs []byte
	scanOne(t, db, &bs, `select randomblob(16)`)
	if len(bs) != 16 {
		t.Errorf("randomblob(16) length=%d, want 16", len(bs))
	}
}

func TestMath_ExtendedFunctions(t *testing.T) {
	db := openDB(t)
	if !hasMathFunctions(t, db) {
		t.Skip("sqlite_math_functions not compiled in")
	}
	cases := []struct {
		sql       string
		want, tol float64
	}{
		{`select sqrt(16)`, 4, 1e-9},
		{`select pow(2, 10)`, 1024, 1e-9},
		{`select power(2, 10)`, 1024, 1e-9},
		{`select exp(0)`, 1, 1e-9},
		{`select exp(1)`, math.E, 1e-9},
		{`select ln(1)`, 0, 1e-9},
		{`select log10(1000)`, 3, 1e-9},
		{`select log2(8)`, 3, 1e-9},
		{`select pi()`, math.Pi, 1e-9},
		{`select sin(0)`, 0, 1e-9},
		{`select cos(0)`, 1, 1e-9},
		{`select tan(0)`, 0, 1e-9},
		{`select asin(1)`, math.Pi / 2, 1e-9},
		{`select acos(1)`, 0, 1e-9},
		{`select atan(0)`, 0, 1e-9},
		{`select atan2(1, 1)`, math.Pi / 4, 1e-9},
		{`select ceil(1.2)`, 2, 1e-9},
		{`select ceiling(1.2)`, 2, 1e-9},
		{`select floor(1.8)`, 1, 1e-9},
		{`select trunc(1.8)`, 1, 1e-9},
		{`select trunc(-1.8)`, -1, 1e-9},
		{`select degrees(0)`, 0, 1e-9},
		{`select radians(180)`, math.Pi, 1e-9},
		{`select mod(10, 3)`, 1, 1e-9},
		{`select sinh(0)`, 0, 1e-9},
		{`select cosh(0)`, 1, 1e-9},
		{`select tanh(0)`, 0, 1e-9},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			var got float64
			scanOne(t, db, &got, c.sql)
			if math.Abs(got-c.want) > c.tol {
				t.Errorf("%s = %f, want %f", c.sql, got, c.want)
			}
		})
	}
}
