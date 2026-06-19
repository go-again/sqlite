---
title: Performance
description: Where the pure-Go engine matches mattn, where it pays a constant factor, and how to measure on your hardware.
sidebar:
  order: 26
---

# Performance

The underlying engine is `modernc.org/sqlite`, whose maintainer publishes numbers against the major C-bound drivers at [The SQLite Drivers Benchmarks Game](https://pkg.go.dev/modernc.org/sqlite-bench#readme-tl-dr-scorecard). Numbers vary by workload; the broad picture is consistent:

- **Bulk read / scan** paths are within single-digit-percent of mattn.
- **Bulk insert** under WAL is comparable.
- **UDF-heavy** workloads where Go callbacks fire on every row pay a measurable constant factor (the ccgo-transpiled call paths go through more indirection than mattn's CGo binding). For a no-op authorizer alongside a tiny SELECT, the overhead measures in the low single-digit-percent range with a handful of extra allocs/op; run `BenchmarkAuthorizer_NoOp` in `bench_test.go` to measure.
- **Connection open** is faster here than mattn — there's no dlopen / dlsym / extension-resolution dance.

The cost-of-doing-business: a hand-tuned C+CGo binding wins on hot UDF paths, and we don't try to beat it. For "use SQL, let SQLite do the heavy lifting" workloads, the choice is mostly about deployment convenience (CGo-free static binaries, trivial cross-compilation), not throughput.

If exact numbers matter for your workload, `go test -bench=. -benchmem -count=5` against your own data is the right primary source — micro-benchmarks from someone else's hardware lie often. Maintainer-side baselines (last-measured numbers + methodology) live in `dev/perf.md`.
