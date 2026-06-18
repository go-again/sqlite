# Coverage: mattn upstream test suite (vendored subset)

We claim drop-in compatibility with `github.com/mattn/go-sqlite3` at the
`database/sql` API surface. To prove that, we vendor a subset of mattn's
own test suite into this repository under the `mattn_upstream` build
tag. The tests live alongside our own (root package,
`mattn_*_upstream_test.go`).

Last reviewed against `github.com/mattn/go-sqlite3` master on
2026-05-26.

## Reproduction

```sh
go test -tags=mattn_upstream -count=1 -timeout 5m .
```

## What's vendored and passing

| File | Status |
|---|---|
| `mattn_sqlite3_opt_math_functions_test_upstream_test.go` | ✓ passes |
| `mattn_sqlite3_opt_unlock_notify_test_upstream_test.go` | ✓ passes |

All vendored tests pass (excluding our own co-located tests that
the tag also picks up).

## What's NOT vendored — and the divergences each surfaces

Vendoring mattn's full suite revealed real API divergences between our
package and mattn. These are honest gaps in our drop-in claim, not bugs
in the tests. Where the divergence is benign (we use methods instead of
fields, or different signatures), we don't break runtime compatibility
of typical user code, but mattn's *tests for those specifics* won't
compile against us.

| File | Why not vendored |
|---|---|
| `sqlite3_test.go` (2690 lines, 44 tests) | `Error.Code` and `Error.ExtendedCode` are methods on our type, fields on mattn's. `Stmt.Readonly`, `Rows.DeclTypes`, `Conn.SetFileControlInt`, the `Version` constant, the `SQLITE_FCNTL_*` constants are mattn-only. `CommitHookFn` and `AuthorizerFn` have different return types (`int32` vs `int`). |
| `sqlite3_sql_test.go` | Uses `Stmt.Readonly`, missing in our package. |
| `error_test.go` | All assertions are `err.(Error)` field accesses against `Error.Code` / `Error.ExtendedCode` as fields. |
| `callback_test.go` | Probes mattn-private callback machinery (`callbackArg`, `callbackRet`, `callbackArgConverter`) that doesn't exist in our package. |
| `sqlite3_opt_preupdate_hook_test.go` | `PreUpdateData.Op` is `int32` for us, `int` for mattn. (We're closer to the C ABI here.) |
| `sqlite3_opt_serialize_test.go` | mattn's `Serialize(dbName string)` vs our `Serialize()`; mattn's `Deserialize([]byte, dbName)` vs our `Deserialize([]byte)`. |
| `sqlite3_opt_vtable_test.go` | Uses mattn's `VTab` / `VTabCursor` / `InfoConstraint` / `InfoOrderBy` / `IndexResult` / `SQLiteContext` types. We use the modernc-style `modernc.org/sqlite/vtab` API instead. |
| `sqlite3_stmt_cache_test.go` | Probes mattn-private `prepareWithCache` and `stmtCache` fields. Our cache is similar in spirit (see `stmt_cache.go`) but the test reaches into mattn-private internals. |
| `backup_test.go` | Our backup completes synchronously where mattn's expects multi-step progress, and `Error.Error()` formats differently ("SQL logic error: foo (1)" vs "foo"). Both are real semantic / wire-format divergences. |
| `sqlite3_opt_fts3_test.go` | modernc's SQLite ships **FTS5 only**. FTS3 and FTS4 are not compiled in. Test reports `no such module: fts3`. |
| `sqlite3_opt_column_metadata_test.go` | `Stmt.ColumnTableName` is mattn-only. |
| `sqlite3_load_extension_test.go` | LoadExtension is platform-skipped on darwin/windows in our package (see `platform_test.go`). Vendoring would just hit the same skips. |
| `sqlite3_func_crypt_test.go` | Tests SQLCipher-style encryption that mattn ships behind a build tag, never compiled here. |
| `sqlite3_opt_userauth_test.go` | Userauth was removed upstream from modernc; we reject `_auth*` DSN flags. See CLAUDE.md. |
| `sqlite3_stmt_cache_bench_test.go` | Benchmark, not coverage. |

## What this validates

The 4 vendored tests confirm that:

- mattn's math UDFs (`acos`, `floor`, etc.) work through our driver.
- mattn's unlock-notify semantics (the `_unlock_notify=1` DSN flag plus
  the busy-handler retry pattern) work through our driver under
  contention, including the deadlock case.

That's a thin but real slice of coverage. Most of mattn's test suite
ends up exercising *mattn-specific types*, not the `database/sql`
interface, so the suite as written is brittle to any driver that
isn't literally mattn. The work to adapt the other tests means either
adding methods/fields to our types (which we won't do where we already
have a cleaner API — `Error.Code()` is the right shape, not `Error.Code`)
or rewriting the assertions, which defeats the "vendored unchanged"
contract.

## What does the practical drop-in claim mean, then?

What matters for migrating off mattn isn't whether mattn's tests run
against us — it's whether *user code* that imports mattn and uses the
`database/sql` interface keeps working. That's covered by:

1. `compat_test.go` in this repo, which exercises the
   `&SQLiteDriver{...}` literal-struct pattern + the type aliases
   (`SQLiteConn`, `SQLiteStmt`, `SQLiteError`, etc.).
2. The migration examples under `examples/migrating/from-mattn/`.
3. The unlock-notify and math-functions tests vendored here.
4. The DSN flag table in `dsn.go::translateMattnDSN` covering all
   `_*` flags mattn defined.

The mattn-upstream lane is a **canary**, not a parity proof.

## When mattn ships a new version

To refresh the vendored set:

1. Pull the upstream tree at the target version:
   `git clone --depth 1 --branch vX.Y.Z https://github.com/mattn/go-sqlite3 /tmp/mattn`
2. Copy the test files you want into our root with the
   `mattn_*_upstream_test.go` naming and a `//go:build mattn_upstream`
   tag prepended; rewrite `package sqlite3` → `package sqlite`.
3. Run `go test -tags=mattn_upstream .` and triage failures against
   the divergence table above — entries where mattn changed shape may
   now match ours, or vice versa.
