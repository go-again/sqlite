# Coverage: modernc upstream test suite (vendored subset)

We forked `modernc.org/sqlite`'s hand-written Go wrapper to add per-conn
APIs. To prove the fork still honors modernc's contract, we vendor a
subset of their test suite into this repository under the
`modernc_upstream` build tag. The tests live alongside our own (root
package, `*_upstream_test.go`) so they have full access to internal
types, but are gated by the build tag so the default `go test ./...`
isn't dragged down by the long-running upstream suite.

Last reviewed against `modernc.org/sqlite v1.50.1` on 2026-05-26.

## Reproduction

```sh
go test -tags=modernc_upstream -count=1 -timeout 5m .
```

## Vendored test files

| File | Status |
|---|---|
| `null_upstream_test.go` | ✓ runs clean |
| `backup_upstream_test.go` | ✓ runs clean |
| `fcntl_upstream_test.go` | ✓ runs clean |
| `module_upstream_test.go` | ✓ runs clean |
| `pre_update_hook_upstream_test.go` | ✓ runs clean |

Each file was copied verbatim from `modernc.org/sqlite@v1.50.1`, with
two mechanical edits:

1. A `//go:build modernc_upstream` tag and a "Vendored from" comment
   prepended.
2. `package sqlite_test` → `package sqlite` (where applicable) so the
   tests run in our package and can reach internal types like `*conn`.
   The single explicit reference to `sqlite.Foo` in
   `pre_update_hook_upstream_test.go` was dropped to bare `Foo` since
   we're already in `package sqlite`.

## Explicitly NOT vendored

| File | Reason |
|---|---|
| `all_test.go` (4525 lines, 64 tests) | Re-execs `go test ... -inner` for `TestConcurrentGoroutines`; pulls `modernc.org/fileutil/ccgo` and `modernc.org/mathutil`; embeds two SQLite test databases. Porting these means rewriting the self-exec machinery to honor our build tag, which is more harness adaptation than driver-validation work. |
| `func_test.go` (1216 lines) | Contains `TestRegisteredFunctions/QueryContext_with_context_expiring`, which hangs against our driver — the test expects context cancellation to interrupt a long-running UDF promptly, and our `interruptOnDone` path doesn't fire fast enough on that exact pattern. Flagged as a real driver gap to investigate; the rest of `func_test.go` was deferred until that's understood. |
| `vec_test.go` | Already covered by our own `vec/raw_test.go`, which mirrors the same fixture (rowids 2,1; distances 2.38687, 2.38978) plus exercises every documented sqlite-vec helper. Vendoring would just duplicate. |
| `leak_test.go` | Uses pprof-driven leak detection that's brittle to harness changes. Low-value validation for our fork. |

## Known bug surfaced by vendoring

While porting `func_test.go`, the sub-test `QueryContext_with_context_expiring`
hung for 30+ seconds rather than completing the cancellation handshake.
Our driver does install a goroutine to call `sqlite3_interrupt` when the
context fires (see `sqlite.go::interruptOnDone`), but this test reveals
that path doesn't fire fast enough in at least one case — probably
because the UDF call path itself doesn't check the interrupt flag at the
right granularity. **TODO**: fix and then drop `func_test.go` back into
the vendor set.

## Why we keep these vendored rather than running modernc's suite directly

Running modernc's tests against modernc itself would prove only that
their bundled SQLite + libc are healthy — not that our fork hasn't drifted
from their contract. Since we modified the wrapper to add per-conn
methods (`RegisterFunc`, `RegisterAuthorizer`, etc.) and reshuffle some
internals, a contract drift would silently pass modernc's own CI.
Vendoring runs the tests against **our** code, against our build tag.

## When modernc bumps

1. `just bump-modernc vX.Y.Z` (this also pulls libc forward).
2. Run `go test -tags=modernc_upstream .` and confirm all vendored
   tests still pass.
3. If a test starts failing, classify: genuine wrapper drift (fix the
   wrapper), upstream behavior change (port the new test version),
   or environment change (skip with rationale).
4. Re-copy any of the deliberately-excluded files where the listed
   excuse no longer applies (e.g. once context cancellation is solid,
   bring `func_test.go` back).
