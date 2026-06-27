# xorm compatibility

`xorm.io/xorm` uses this package as its SQLite driver with **no xorm-specific code**. `xorm.NewEngine(name, dsn)` uses the same `name` for both `sql.Open(name, dsn)` and its dialect lookup; xorm registers `sqlite3`, `sqlite`, and `libsql` → its single, driver-agnostic SQLite dialect, and we register our `database/sql` driver under both `sqlite` and `sqlite3`. So `xorm.NewEngine("sqlite3", dsn)` (or `"sqlite"`) drives xorm's dialect with our CGo-free driver, and `_pragma=` / URI DSN flags flow straight through (xorm's `Parse` only derives a display name; the full DSN reaches `sql.Open`).

## The CI gate

The `submodules` CI matrix (matrix entry `xorm-compat`) runs `go test ./...` inside the isolated `xorm-compat/` module — a separate `go.mod` with `replace gosqlite.org => ..`, so `xorm.io/xorm` never enters the main module's graph (a consumer importing `gosqlite.org` carries no xorm at all). `go build ./...` / `go test ./...` from the repo root skip it because it is a separate module. It exercises xorm's public surface against our driver:

- **CRUD** — `Sync2`, `Insert`, `Get`, `Where`, `Find`/`OrderBy`, `ID().Cols().Update()`, `Count`, `Delete`.
- **Types** — int / int64 / float64 / bool / string / `[]byte` / `time.Time` (incl. a `created` column) round-trip.
- **Transactions** — `NewSession` + `Begin` / `Commit` / `Rollback`.
- **Introspection** — `DBMetas` → table + column discovery (xorm parses `sqlite_master`).
- **DSN flags** — `PRAGMA foreign_keys` reports `1` and a FK violation is rejected, proving our `_pragma=foreign_keys(1)` flag reached the driver.

This depends only on the xorm **library** (`xorm.io/xorm` + `xorm.io/builder` + a few pure-Go support modules), not on xorm's own test module — so no CGo and no third-party DB drivers enter our graph.

## Why not run xorm's own `tests/` suite

xorm's `tests/` package blank-imports eight DB drivers in one file (`engine_test.go`), including the CGo `mattn/go-sqlite3` and a `gitee.com`-hosted driver that is frequently unreachable from CI. Running it against our driver is possible — clone xorm, strip the `mattn` / `modernc` / `dm` blank imports from `engine_test.go`, add a blank import of `gosqlite.org`, `replace gosqlite.org => <workspace>`, then `go test ./tests -db=sqlite3` — but it trades the reliable, hermetic gate above for CGo + a flaky external host, and surfaces xorm-internal SQLite expectations unrelated to the driver. Kept as a documented option, not the default gate.

## Running it

`just xorm-compat` (or `cd xorm-compat && go test ./...`) runs the suite locally. The drop-in itself is just a blank import of `gosqlite.org` plus `xorm.NewEngine("sqlite3", dsn)` — the runnable snippet lives in [the migration guide](../../docs/guides/migrating.md#with-xorm).
