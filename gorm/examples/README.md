# `gosqlite.org/gorm` — examples

Runnable examples for the `gosqlite.org/gorm` dialector module — a CGo-free drop-in for `glebarez/sqlite` and `go-gorm/sqlite`. Run one with `go run ./<dir>`.

| example | what it shows |
|---|---|
| [`getting-started`](getting-started/) | the dialector opened from a typed `sqlite.Config` via `sqlitegorm.OpenConfig` — the recommended entry |
| [`from-glebarez`](from-glebarez/) | migrating from `glebarez/sqlite` / `gorm.io/driver/sqlite`: swap the import; `sqlite.Open(dsn)` is identical |
| [`vfs`](vfs/) | gorm reading from an in-memory `fs.FS` via `vfs/` |
| [`ext-scalars`](ext-scalars/) | drive `ext/` scalar / aggregate / collation functions through plain gorm `Where`/`Order`/`Select` |
| [`ext-vtabs`](ext-vtabs/) | query `ext/` virtual tables (series, csv, …) through gorm |
| [`vec`](vec/) | gorm alongside the gorm-free `vec` primitive (sqlite-vec KNN) driven via raw SQL |
| [`fts`](fts/) | gorm alongside the gorm-free `fts` primitive (FTS5 search) driven via raw SQL |

For an ORM with **native**, tag-driven vector / full-text search (declarative `vec:` / `fts:` tags, `AutoMigrate`-provisioned sidecars, typed ranked results), see [**LiteORM**](https://liteorm.org).
