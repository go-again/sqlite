# Performance baselines

Throughput / latency numbers for the post-v0.4 hot paths. Last measured on Apple M4 / darwin/arm64 with Go 1.26 + modernc.org/sqlite at the repo's go.mod-pinned versions, on 2026-05-29. Re-run via `just bench` (or `go test -bench=. -benchmem ./...`) after a dep bump.

## sqlite.Pointer binding

| Benchmark | ns/op | B/op | allocs/op | Notes |
|---|---:|---:|---:|---|
| `Pointer_BindRelease` | 1122 | 208 | 10 | Full SQL round-trip: prepare → bind → exec → finalize → destructor |
| `Pointer_Registry_StoreLoad` | 116 | 24 | 1 | Direct registry store + load + release; isolates the mutex-guarded path |

~1 μs end-to-end per Pointer bind through a UDF. The 10 allocs/op are split between database/sql's NamedValue conversion (4), our `*pointerArg` wrapper + registry entry (3), and modernc's prepared-stmt path (3). The registry-only path is one alloc (the map entry).

## ext/stats

| Benchmark | ns/op | rows/sec | Notes |
|---|---:|---:|---|
| `Stats_VarPop_100K` | 26,771,530 | 3.7 M | Streaming Welford with Kahan compensation |
| `Stats_RegrSlope_100K` | 31,464,108 | 3.2 M | welford2 bivariate accumulator |
| `Stats_PercentileCont_10K` | 4,446,285 | 2.2 M | Buffered + sorted at Value time |

The Welford accumulator scales linearly. Bivariate (regr_slope, corr, covar) costs ~15% more than univariate (var_pop, stddev_pop) because of the second running sum + cross-product. The percentile path is bounded by `sort.Float64s` — O(N log N) per `Value` call.

## ext/csv

| Benchmark | ns/op | rows/sec | Notes |
|---|---:|---:|---|
| `CSV_FullScan_10K` | 2,402,892 | 4.2 M | `SELECT COUNT(*)` over a 10K-row in-memory CSV; tests the streaming Filter path |
| `CSV_Filtered_10K` | 1,343,563 | 7.4 M | `WHERE k = 5000` short-circuits scanning after the matching row |

Throughput is dominated by `encoding/csv.Read` + the per-row Column callback into SQLite. CSV is a full-scan vtab — there's no index — so the planner reads every row, but it can stop early on a filter that matches.

## ext/bloom

| Benchmark | ns/op | per-op throughput | Notes |
|---|---:|---:|---|
| `Bloom_Insert_10K` | 18,185,301 | 550 K inserts/sec | Per-iteration: fresh filter + 10K inserts via prepared stmt |
| `Bloom_Membership_10K` | 2,826 | 354 K lookups/sec | Membership test, 50% hit / 50% miss |

Bloom membership lookups cost ~2.8 μs each, dominated by the 3 hash function evaluations per the optimal k=`round(-log2(p))` calculation for p=0.01. Insert is more expensive because each Insert acquires the table write lock; for bulk-loading wrap inserts in a single explicit transaction.

## How to read these numbers

These are wall-clock results on a fast machine; CI numbers will be different and that's fine. The intent is to set a baseline so a perf regression is visible after a dep bump or refactor.

| What is | What isn't |
|---|---|
| End-to-end (driver + libc + SQLite + Go-side accumulators) | Micro-benchmarks of individual functions in isolation |
| Single-conn pool (MaxOpenConns=1) | Concurrent multi-conn throughput |
| In-memory (`:memory:`) DB | File-backed DB with WAL syncs |
| The hot path the user pays for | All paths exhaustively |
