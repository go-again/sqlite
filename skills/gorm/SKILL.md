---
name: gorm
description: Use when using gorm.io/gorm with gosqlite — the gosqlite.org/gorm dialector, a CGo-free drop-in for glebarez/sqlite and gorm.io/driver/sqlite, shipped as its own module.
---

# gorm with gosqlite

`gosqlite.org/gorm` is a `gorm.Dialector` — a drop-in for `glebarez/sqlite` and `gorm.io/driver/sqlite`. It is its **own module** (`go get gosqlite.org/gorm`); the gosqlite core does not depend on gorm.

```go
import (
	"gorm.io/gorm"
	sqlite "gosqlite.org/gorm" // package is `sqlite` — drop-in for glebarez/sqlite + gorm.io/driver/sqlite
)

db, _ := gorm.Open(sqlite.Open("file:my.db?_pragma=foreign_keys(1)"), &gorm.Config{})
```

The package name is `sqlite`, so existing glebarez / go-gorm call sites compile unchanged: `sqlite.Open(dsn)` and `sqlite.New(Config{...})` both exist.

For the modern typed config, also import the root `gosqlite.org` (also `package sqlite`) and alias the dialector `sqlitegorm`:

```go
import (
	sqlite "gosqlite.org"
	sqlitegorm "gosqlite.org/gorm"
)

db, _ := sqlitegorm.OpenConfig(sqlite.Config{Path: "my.db", Pragmas: sqlite.RecommendedPragmas()})
```

## Good to know

- **`OpenConfig` returns a `*sqlitegorm.DB`** that embeds `*gorm.DB`; `defer db.Close()` drains the pool — no separate `*sql.DB` teardown.
- **No encryption via the dialector** — for an encrypted database with an ORM, use [LiteORM](https://liteorm.org) (built on this driver), which integrates `vfs/crypto` itself.
- **Error translation** is on through the dialector: unique / primary-key violations map to `gorm.ErrDuplicatedKey`, foreign-key violations to `gorm.ErrForeignKeyViolated`.
- **`time.Time` columns**: the dialector injects `_texttotime=1` so `DATE` / `DATETIME` / `TIMESTAMP` scan back as `time.Time`; opt out with `_texttotime=0` in the DSN.
- **ext/ virtual tables** (`csv`, `lines`, `closure`, `bloom`, `spellfix1`, …) work through gorm: register the module on the pool, pin it with `sqlDB.SetMaxOpenConns(1)`, create the vtab with `db.Exec` (AutoMigrate can't emit `CREATE VIRTUAL TABLE`), then read with `db.Raw` / `db.Table(...).Scan`. See [`gorm/examples/ext-vtabs`](../../gorm/examples/ext-vtabs/main.go).

Runnable demos: [`gorm/examples/`](../../gorm/examples/) (getting-started, from-glebarez, vfs, ext-scalars, ext-vtabs).
