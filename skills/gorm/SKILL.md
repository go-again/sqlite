---
name: gorm
description: Use when using gorm.io/gorm with gosqlite — the gosqlite.org/gorm dialector, a CGo-free drop-in for glebarez/go-gorm-sqlite, shipped as its own module.
---

# gorm with gosqlite

`gosqlite.org/gorm` is a `gorm.Dialector` — a drop-in for `glebarez/sqlite` and `go-gorm/sqlite`. It is its **own module** (`go get gosqlite.org/gorm`); the gosqlite core does not depend on gorm.

```go
import (
	"gorm.io/gorm"
	sqlite "gosqlite.org/gorm" // package is `sqlite` — drop-in for glebarez/go-gorm
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

## Vector / full-text search

There are **no vec/FTS gorm sidecar plugins** in gosqlite — do not reach for `vec/gorm` or `fts/gorm` (removed). For an ORM with native vector/full-text search, use **liteorm** (`liteorm.org`): declarative `vec:` / `fts:` tags + `AutoMigrate` + typed `search.For[T](db).Vector/.FullText/.Hybrid` helpers. On plain `gorm.io/gorm`, drive gosqlite's gorm-free `vec` / `fts` primitives via raw SQL (`db.Exec` / `db.Raw`).
