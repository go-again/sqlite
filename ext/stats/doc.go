// Package stats provides PostgreSQL-style statistical aggregate and
// window functions plus two scalar helpers. Pure Go on top of our
// streaming Welford / Terriberry algorithms with Kahan compensation —
// numerically stable, supports SQLite's sliding-window Inverse path.
//
// # Aggregates and window functions
//
//   - var_pop(x), var_samp(x) — variance (population / sample)
//   - stddev_pop(x), stddev_samp(x) — standard deviation
//   - skewness_pop(x), skewness_samp(x) — Pearson skewness
//   - kurtosis_pop(x), kurtosis_samp(x) — Fisher excess kurtosis
//   - covar_pop(y, x), covar_samp(y, x) — covariance
//   - corr(y, x) — Pearson correlation
//   - regr_r2(y, x), regr_avgx(y, x), regr_avgy(y, x) — regression
//   - regr_sxx(y, x), regr_syy(y, x), regr_sxy(y, x), regr_count(y, x)
//   - regr_slope(y, x), regr_intercept(y, x) — least-squares fit
//   - regr_json(y, x) — every regr_* result bundled as a JSON object
//   - percentile_cont(x, p) — continuous percentile (linear interpolation)
//   - percentile_disc(x, p) — discrete percentile (nearest sample)
//   - percentile(x, p) — alias for percentile_cont with p in [0, 100]
//   - median(x) — alias for percentile_cont(x, 0.5)
//   - mode(x) — most frequent value (deterministic tiebreak by sort order)
//   - every(b), some(b) — boolean AND / OR aggregates
//
// Every aggregate doubles as a window function: SQLite's window machinery
// drives the streaming Step / Inverse / Value lifecycle without
// recomputing from scratch on each frame. Welford + Kahan plus
// Terriberry for higher moments — same algorithms ncruces ships and the
// SQLite-bundled `percentile.c` uses. Cumulative percentile / median use
// a buffer + sort on each Value call (O(N log N) per emit for sliding
// frames; O(N log N) once for non-windowed aggregation).
//
// # Scalar helpers
//
//   - cbrt(x) — cube root via [math.Cbrt]
//   - cot(x) — cotangent (1/tan x); returns NaN for x = 0
//
// # Usage
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/ext/stats"
//	)
//
//	if err := stats.Register(conn); err != nil { ... }
//
//	// Population variance grouped by department:
//	rows, _ := db.QueryContext(ctx, `
//	    SELECT dept, var_pop(salary), corr(salary, tenure)
//	    FROM employees GROUP BY dept`)
//
//	// Windowed regr_slope over a 5-row sliding frame:
//	rows, _ := db.QueryContext(ctx, `
//	    SELECT t, regr_slope(y, x) OVER (
//	        ORDER BY t ROWS BETWEEN 2 PRECEDING AND 2 FOLLOWING
//	    ) FROM samples`)
//
// For pool-wide install via [github.com/go-again/sqlite.Driver.ConnectHook],
// blank-import the auto sub-package:
//
//	import _ "github.com/go-again/sqlite/ext/stats/auto"
//
// # Empty-set semantics
//
// Following PostgreSQL: aggregates over zero rows return SQL NULL.
// Single-row aggregates whose definition divides by (n-1) — var_samp,
// stddev_samp, regr_count exceptions noted in source — return NULL when
// the count is too small to be well-defined. mode([]) returns NULL.
//
// Ported from [ncruces/ext/stats]. The algorithm implementations are
// straight transliterations of ncruces's Welford / Terriberry / Kahan
// math; the registration shape adapts to our [Conn.RegisterWindowFunction]
// surface instead of ncruces's `CreateWindowFunction`.
//
// [ncruces/ext/stats]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/stats
package stats
