---
title: Migrating
description: Drop-in recipes from mattn/go-sqlite3, modernc.org/sqlite, glebarez/sqlite, go-gorm/sqlite, and ncruces/go-sqlite3.
sidebar:
  order: 3
---

# Migrating from another SQLite package

| Old import | New import | Notes |
|---|---|---|
| `_ "github.com/mattn/go-sqlite3"` | `_ "gosqlite.org"` | `sql.Open("sqlite3", ...)` keeps working. |
| `_ "modernc.org/sqlite"` | `_ "gosqlite.org"` | `sql.Open("sqlite", ...)` keeps working. |
| `"github.com/glebarez/sqlite"` | `"gosqlite.org/gorm"` | `sqlite.Open(dsn)` keeps working. |
| `"github.com/go-gorm/sqlite"` | `"gosqlite.org/gorm"` | `sqlite.New(sqlite.Config{...})` keeps working. |

Runnable migration examples live under [`examples/migrating/`](../../examples/migrating/).

## From mattn/go-sqlite3

Change one line:

```diff
- import _ "github.com/mattn/go-sqlite3"
+ import _ "gosqlite.org"
```

Everything else — `sql.Open("sqlite3", ...)`, the `_*` DSN flags, custom-driver registration via `&sqlite3.SQLiteDriver{Extensions, ConnectHook}`, `Conn.RegisterFunc`, `errors.Is(err, sqlite.ErrConstraintUnique)` — works unchanged. The `_auth*` userauth flags are rejected (that feature was dropped upstream). See [`examples/migrating/from-mattn/`](../../examples/migrating/from-mattn/main.go).

## From modernc.org/sqlite

Swap the import; same `"sqlite"` driver name and DSN — nothing else changes.

```diff
- import _ "modernc.org/sqlite"
+ import _ "gosqlite.org"
```

See [`examples/migrating/from-modernc/`](../../examples/migrating/from-modernc/main.go).

## From glebarez/sqlite or go-gorm/sqlite (gorm dialectors)

Swap the dialector import; the `sqlite.Open(dsn)` / `sqlite.New(Config{...})` signatures are identical, and `TranslateError` behaves the same.

```go
import (
	"gorm.io/gorm"
	sqlite "gosqlite.org/gorm"
)

db, _ := gorm.Open(sqlite.Open("file:my.db?_pragma=foreign_keys(1)"), &gorm.Config{})
```

See [`gorm/examples/from-glebarez/`](../../gorm/examples/from-glebarez/main.go) and the [gorm guide](gorm.md).

## With xorm

`xorm.io/xorm` works out of the box — it maps the `"sqlite3"`, `"sqlite"`, and `"libsql"` driver names to its driver-agnostic SQLite dialect, and we register under both `"sqlite"` and `"sqlite3"`. Blank-import the driver and call `NewEngine` exactly as you would for mattn or modernc; your `_pragma=` DSN flags still apply.

```go
import (
	_ "gosqlite.org" // registers "sqlite" + "sqlite3"
	"xorm.io/xorm"
)

eng, _ := xorm.NewEngine("sqlite3", "file:app.db?_pragma=foreign_keys(1)")
```

Compatibility is CI-enforced by the `xorm-compat` job.

## From ncruces/go-sqlite3

Partial migration only — not a one-line swap. `ncruces/go-sqlite3` is the other major CGo-free Go SQLite driver (WebAssembly via wazero, vs our ccgo-transpiled Go), with a different public API for everything beyond `database/sql`.

What works after the import swap:

```diff
- import _ "github.com/ncruces/go-sqlite3/driver"
+ import _ "gosqlite.org"
```

- `sql.Open("sqlite3", dsn)` — same driver name.
- URI-form DSNs with repeatable `_pragma=…` — accepted verbatim. You additionally gain the mattn-style short forms (`_fk=1`, `_journal=WAL`, `_busy_timeout=N`, …) that ncruces doesn't support.
- Standard `database/sql` — `db.Query`, `db.Exec`, `db.BeginTx`, prepared statements — all unchanged.

What needs rewriting:

- Anything importing `github.com/ncruces/go-sqlite3` for type names (`sqlite3.Conn`, `sqlite3.Context`, `sqlite3.Value`, `sqlite3.ScalarFunction`) — those types don't exist here. Move per-conn work to `db.Conn(ctx)` + `conn.Raw(func(dc any) error { c := dc.(*sqlite.Conn); … })`.
- `driver.Open(dsn, func(*sqlite3.Conn) error)` — no equivalent constructor; use mattn-style `&sqlite.SQLiteDriver{ConnectHook: ...}` registration, or install hooks per-conn after `db.Conn(ctx)`.
- UDF / aggregator / collation / window / hook callsites — map as `CreateFunction` → `Conn.RegisterFunc(name, fn, pure)`, `CreateAggregateFunction` → `Conn.RegisterAggregator`, `CreateWindowFunction` → `Conn.RegisterWindowFunction(name, nArg, ctor, pure)`, `CreateCollation` → `Conn.RegisterCollation`, `UpdateHook` → `RegisterUpdateHook`, `SetAuthorizer` → `RegisterAuthorizer`, `Trace` → `SetTrace`. Signatures differ.
- `gormlite.Open(dsn)` → `sqlitegorm.Open(dsn)` (textual swap, glebarez-compatible).
- `vfs/readervfs.Create(...)` → `vfs.New(fs.FS) (name, *vfs.FS, error)` — different signature, same intent.
- `ext/vec1` users — our `vec/` wraps [sqlite-vec](https://github.com/asg017/sqlite-vec) (asg017's), not vec1 (SQLite-org's). The vtab name and SQL surface differ; rewrite SQL, not just imports.
- Adiantum / XTS encryption — ncruces exposes `vfs/adiantum` and `vfs/xts`; ours is `vfs/crypto` with both ciphers behind one `New(Options{Cipher: …})`. Same threat model.

If you're a pure-`database/sql` consumer with `_pragma=…` URI DSNs and no custom UDFs, this is a one-line swap. Otherwise budget for a per-call-site rewrite.

## Coexisting with mattn in one binary

Both packages claim `"sqlite3"` by default, so blank-importing both panics at init. To run both (gradual migration, a mattn-only `.so` extension), register this one under a custom name — see [Coexistence](../reference/driver-names.md#coexistence-with-mattn).
