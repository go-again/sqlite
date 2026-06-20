---
title: gorm integration
description: The gosqlite.org/gorm dialector — a CGo-free drop-in for glebarez and go-gorm/sqlite, shipped as its own module.
sidebar:
  order: 4
---

# gorm

`gosqlite.org/gorm` is a `gorm.Dialector` — a drop-in for both `glebarez/sqlite` and the official `go-gorm/sqlite`. It ships as its **own module**, so the gosqlite core stays free of `gorm.io/gorm`; add it with `go get gosqlite.org/gorm`.

```go
import (
	"gorm.io/gorm"
	sqlite "gosqlite.org/gorm"
)

db, _ := gorm.Open(sqlite.Open("file:my.db?_pragma=foreign_keys(1)"), &gorm.Config{})
```

`sqlite.Open(dsn)` and `sqlite.New(sqlite.Config{...})` are both provided so either upstream import path swaps in unchanged. For the modern typed entry, also import the root `gosqlite.org` (also `package sqlite`) and alias the dialector `sqlitegorm`:

```go
import (
	sqlite "gosqlite.org"
	sqlitegorm "gosqlite.org/gorm"
)

db, _ := sqlitegorm.OpenConfig(sqlite.Config{Path: "my.db", Pragmas: sqlite.RecommendedPragmas()})
```

See [Configuration](configuration.md); coverage matrix: [`dev/coverage/gorm.md`](../../dev/coverage/gorm.md).

## Vector & full-text search in an ORM

For an ORM with **native** vector (sqlite-vec) and full-text (FTS5) search — declarative `vec:` / `fts:` tags, `AutoMigrate`-provisioned sidecars kept in sync on every write, and typed ranked results — use **[liteorm](https://liteorm.org)**, the modern data layer built on gosqlite. It supersedes the earlier tag-driven gorm bridges with a first-class implementation.

If you're staying on `gorm.io/gorm`, drive gosqlite's gorm-free [`vec`](vector-search.md) / [`fts`](full-text-search.md) primitives directly via `db.Exec` / `db.Raw` over the underlying `*sql.DB`.

Runnable: [`gorm/examples/getting-started/`](../../gorm/examples/getting-started/main.go) and [`gorm/examples/from-glebarez/`](../../gorm/examples/from-glebarez/main.go).
