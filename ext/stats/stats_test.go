package stats_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/stats"
)

func openDB(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, err := sql.Open(sqlite.DriverName, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return stats.Register(c)
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := sc.ExecContext(context.Background(), `
		CREATE TABLE samples(x REAL, y REAL);
		INSERT INTO samples(x, y) VALUES
		    (1.0, 2.0), (2.0, 4.0), (3.0, 6.0),
		    (4.0, 8.0), (5.0, 10.0);`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return db, sc
}

func nearly(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return false
	}
	return math.Abs(a-b) < 1e-9
}

func TestStats_Variance(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	cases := []struct {
		fn   string
		want float64
	}{
		{"var_pop(x)", 2.0},  // ((1-3)²+(2-3)²+(3-3)²+(4-3)²+(5-3)²) / 5 = 10/5
		{"var_samp(x)", 2.5}, // /4 (Bessel)
		{"stddev_pop(x)", math.Sqrt(2.0)},
		{"stddev_samp(x)", math.Sqrt(2.5)},
	}
	for _, tc := range cases {
		var got float64
		if err := sc.QueryRowContext(ctx,
			"SELECT "+tc.fn+" FROM samples").Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.fn, err)
		}
		if !nearly(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.fn, got, tc.want)
		}
	}
}

func TestStats_CovarianceAndCorr(t *testing.T) {
	// y = 2x → perfect correlation, covar = var_pop(x) * 2.
	_, sc := openDB(t)
	ctx := context.Background()
	var c, p, s float64
	if err := sc.QueryRowContext(ctx,
		`SELECT corr(y, x), covar_pop(y, x), covar_samp(y, x) FROM samples`).Scan(&c, &p, &s); err != nil {
		t.Fatal(err)
	}
	if !nearly(c, 1.0) {
		t.Errorf("corr=%v, want 1.0", c)
	}
	if !nearly(p, 4.0) { // 2 * var_pop(x) = 4
		t.Errorf("covar_pop=%v, want 4", p)
	}
	if !nearly(s, 5.0) { // 2 * var_samp(x) = 5
		t.Errorf("covar_samp=%v, want 5", s)
	}
}

func TestStats_RegrSlope(t *testing.T) {
	// y = 2x + 0 → slope=2, intercept=0.
	_, sc := openDB(t)
	var slope, intercept float64
	if err := sc.QueryRowContext(context.Background(),
		`SELECT regr_slope(y, x), regr_intercept(y, x) FROM samples`).Scan(&slope, &intercept); err != nil {
		t.Fatal(err)
	}
	if !nearly(slope, 2.0) {
		t.Errorf("regr_slope=%v, want 2.0", slope)
	}
	if !nearly(intercept, 0.0) {
		t.Errorf("regr_intercept=%v, want 0.0", intercept)
	}
}

func TestStats_RegrJSON(t *testing.T) {
	_, sc := openDB(t)
	var got string
	if err := sc.QueryRowContext(context.Background(),
		`SELECT regr_json(y, x) FROM samples`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	var obj map[string]float64
	if err := json.Unmarshal([]byte(got), &obj); err != nil {
		t.Fatalf("not JSON: %q  err=%v", got, err)
	}
	if !nearly(obj["slope"], 2.0) || !nearly(obj["count"], 5) {
		t.Errorf("regr_json = %q, want slope=2 count=5", got)
	}
}

func TestStats_Median(t *testing.T) {
	_, sc := openDB(t)
	var got float64
	if err := sc.QueryRowContext(context.Background(),
		`SELECT median(x) FROM samples`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !nearly(got, 3.0) {
		t.Errorf("median=%v, want 3", got)
	}
}

func TestStats_Percentile(t *testing.T) {
	// percentile_cont(x, 0.5) ≡ median; with 5 sorted values 1..5,
	// the 0-indexed position 0.5*(5-1)=2 → exact value 3.
	_, sc := openDB(t)
	ctx := context.Background()
	cases := []struct {
		q    string
		want float64
	}{
		{`SELECT percentile_cont(x, 0.5) FROM samples`, 3.0},
		{`SELECT percentile_cont(x, 0.25) FROM samples`, 2.0},
		{`SELECT percentile_disc(x, 0.4) FROM samples`, 2.0},
		{`SELECT percentile(x, 50) FROM samples`, 3.0}, // /100
	}
	for _, tc := range cases {
		var got float64
		if err := sc.QueryRowContext(ctx, tc.q).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.q, err)
		}
		if !nearly(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.q, got, tc.want)
		}
	}
}

func TestStats_PercentileJSON(t *testing.T) {
	_, sc := openDB(t)
	var got string
	if err := sc.QueryRowContext(context.Background(),
		`SELECT percentile_cont(x, '[0.25, 0.5, 0.75]') FROM samples`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	var arr []float64
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("not JSON array: %q", got)
	}
	if len(arr) != 3 || !nearly(arr[0], 2.0) || !nearly(arr[1], 3.0) || !nearly(arr[2], 4.0) {
		t.Errorf("got %v, want [2 3 4]", arr)
	}
}

func TestStats_Mode_Integer(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE m(v); INSERT INTO m(v) VALUES (1), (2), (2), (3), (3), (3)`); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := sc.QueryRowContext(context.Background(),
		`SELECT mode(v) FROM m`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Errorf("mode=%d, want 3", got)
	}
}

func TestStats_Mode_Text(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE m(v); INSERT INTO m(v) VALUES ('a'), ('b'), ('b')`); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := sc.QueryRowContext(context.Background(),
		`SELECT mode(v) FROM m`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "b" {
		t.Errorf("mode=%q, want %q", got, "b")
	}
}

func TestStats_EveryAndSome(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE TABLE b(v); INSERT INTO b(v) VALUES (1), (1), (0)`); err != nil {
		t.Fatal(err)
	}
	var every, some bool
	if err := sc.QueryRowContext(ctx, `SELECT every(v), some(v) FROM b`).Scan(&every, &some); err != nil {
		t.Fatal(err)
	}
	if every {
		t.Error("every should be false (0 in set)")
	}
	if !some {
		t.Error("some should be true")
	}
}

func TestStats_EmptySetReturnsNULL(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(), `CREATE TABLE e(v REAL)`); err != nil {
		t.Fatal(err)
	}
	cases := []string{"var_pop(v)", "median(v)", "mode(v)", "corr(v, v)"}
	for _, fn := range cases {
		var v sql.NullFloat64
		if err := sc.QueryRowContext(context.Background(),
			"SELECT "+fn+" FROM e").Scan(&v); err != nil {
			// mode returns potentially non-numeric; allow ErrInvalidScan.
			if !strings.Contains(err.Error(), "Scan") {
				t.Fatalf("%s: %v", fn, err)
			}
			continue
		}
		if v.Valid {
			t.Errorf("%s = %v, want NULL", fn, v.Float64)
		}
	}
}

func TestStats_WindowFrame(t *testing.T) {
	// Sliding 3-row frame over y=2x: regr_slope inside the frame should
	// stay 2 because the linear relationship is global.
	_, sc := openDB(t)
	rows, err := sc.QueryContext(context.Background(), `
		SELECT regr_slope(y, x) OVER (
		    ORDER BY x ROWS BETWEEN 1 PRECEDING AND 1 FOLLOWING
		) FROM samples`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seen int
	for rows.Next() {
		var v sql.NullFloat64
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		seen++
		if v.Valid && !nearly(v.Float64, 2.0) {
			t.Errorf("row %d: slope=%v, want 2", seen, v.Float64)
		}
	}
	if seen != 5 {
		t.Errorf("seen %d rows, want 5", seen)
	}
}

func TestStats_SkewKurtosisShape(t *testing.T) {
	// Symmetric uniform 1..5 → skewness ≈ 0, kurtosis (Fisher excess)
	// ≈ -1.3 for the uniform distribution.
	_, sc := openDB(t)
	var skew, kurt float64
	if err := sc.QueryRowContext(context.Background(),
		`SELECT skewness_pop(x), kurtosis_pop(x) FROM samples`).Scan(&skew, &kurt); err != nil {
		t.Fatal(err)
	}
	if math.Abs(skew) > 1e-9 {
		t.Errorf("skewness=%v, want ~0", skew)
	}
	if !(kurt > -2 && kurt < 0) {
		t.Errorf("kurtosis=%v, want roughly in (-2, 0)", kurt)
	}
}

// TestStats_VarSampSingleSample pins the special-case: var_samp /
// stddev_samp on a single row return NULL (n-1 = 0 → undefined).
func TestStats_VarSampSingleSample(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE one(x); INSERT INTO one VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	var vs, ss sql.NullFloat64
	if err := sc.QueryRowContext(context.Background(),
		`SELECT var_samp(x), stddev_samp(x) FROM one`).Scan(&vs, &ss); err != nil {
		t.Fatal(err)
	}
	if vs.Valid {
		t.Errorf("var_samp on 1 row = %v, want NULL", vs.Float64)
	}
	if ss.Valid {
		t.Errorf("stddev_samp on 1 row = %v, want NULL", ss.Float64)
	}
}

// TestStats_RegrCountReturnsInteger pins that regr_count returns the
// SQL integer type, scannable into int64 without coercion.
func TestStats_RegrCountReturnsInteger(t *testing.T) {
	_, sc := openDB(t)
	var n int64
	if err := sc.QueryRowContext(context.Background(),
		`SELECT regr_count(y, x) FROM samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("regr_count = %d, want 5", n)
	}
	var typ string
	if err := sc.QueryRowContext(context.Background(),
		`SELECT typeof(regr_count(y, x)) FROM samples`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "integer" {
		t.Errorf("typeof(regr_count) = %q, want integer", typ)
	}
}

// TestStats_EveryAllNullReturnsNULL pins that every / some over a set
// containing only NULLs returns NULL (no rows counted), matching
// PostgreSQL.
func TestStats_EveryAllNullReturnsNULL(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE n(v); INSERT INTO n VALUES (NULL), (NULL), (NULL)`); err != nil {
		t.Fatal(err)
	}
	var e, s sql.NullBool
	if err := sc.QueryRowContext(context.Background(),
		`SELECT every(v), some(v) FROM n`).Scan(&e, &s); err != nil {
		t.Fatal(err)
	}
	if e.Valid {
		t.Errorf("every(all NULL) = %v, want NULL", e.Bool)
	}
	if s.Valid {
		t.Errorf("some(all NULL) = %v, want NULL", s.Bool)
	}
}

// TestStats_ModeIgnoresNULL pins that mode skips NULL inputs — they
// don't bias the frequency count.
func TestStats_ModeIgnoresNULL(t *testing.T) {
	_, sc := openDB(t)
	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE m(v); INSERT INTO m VALUES (NULL), (1), (NULL), (1), (2)`); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := sc.QueryRowContext(context.Background(),
		`SELECT mode(v) FROM m`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Errorf("mode = %d, want 1 (NULL skipped)", got)
	}
}

func TestStats_ScalarHelpers(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	var cbrt float64
	if err := sc.QueryRowContext(ctx, `SELECT cbrt(27)`).Scan(&cbrt); err != nil {
		t.Fatal(err)
	}
	if !nearly(cbrt, 3.0) {
		t.Errorf("cbrt(27)=%v, want 3", cbrt)
	}
	var cot float64
	if err := sc.QueryRowContext(ctx, `SELECT cot(1)`).Scan(&cot); err != nil {
		t.Fatal(err)
	}
	if !nearly(cot, 1/math.Tan(1)) {
		t.Errorf("cot(1)=%v, want %v", cot, 1/math.Tan(1))
	}
}
