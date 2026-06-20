---
name: using-gosqlite
description: Use when opening a SQLite database in Go with gosqlite.org, choosing the "sqlite" vs "sqlite3" driver name, or deciding which sub-package to reach for. The starting point for any task using this driver.
---

# Using gosqlite

A CGo-free SQLite driver + ecosystem for Go. Install: `go get gosqlite.org`. No CGo, no C toolchain.

## Opening a database

The driver registers under two names (same singleton): `"sqlite"` (use for new code) and `"sqlite3"` (mattn-compat). Three levels of setup:

```go
// 1. Plain database/sql.
import _ "gosqlite.org"
db, _ := sql.Open("sqlite", "file:app.db")

// 2. One-line presets (root pkg embeds *sql.DB).
import sqlite "gosqlite.org"
db, _ := sqlite.OpenWAL("app.db")        // WAL + busy_timeout=5s + foreign_keys=on
mem, _ := sqlite.OpenInMemory()          // private in-memory
shared, _ := sqlite.OpenShared("name")   // shared in-memory (multi-conn tests)
ro, _ := sqlite.OpenReadOnly("seed.db")  // refuses to create/write

// 3. Typed Config — pragmas, encryption, pool tuning in one value.
db, _ := sqlite.Open(sqlite.Config{
	Path:    "app.db",
	Pragmas: sqlite.RecommendedPragmas(),
	MaxOpenConns: 8,
})
```

Prefer typed pragma enums (`sqlite.JournalWAL`, `sqlite.SynchronousNormal`, …) over string literals. Legacy DSN strings (`"file:app.db?_pragma=…&_fk=1"`) still work.

For an in-memory database that multiple connections must share, use `OpenShared(name)` or set `db.SetMaxOpenConns(1)` — a plain `:memory:` is private per connection.

## Which sub-package?

| Goal | Package | Skill |
|---|---|---|
| ORM | `gosqlite.org/gorm` | `gorm` |
| Vector / similarity search | `gosqlite.org/vec` | `vector-search` |
| Full-text search | `gosqlite.org/fts` | `full-text-search` |
| Combine vec + fts results | `gosqlite.org/fusion` | `hybrid-search` |
| Encryption / checksums / in-memory / custom storage | `gosqlite.org/vfs/...` | `encryption-and-vfs`, `custom-vfs` |
| Bound the page-cache heap | `gosqlite.org/pcache` | (page cache: `pcache.InstallBoundedLRU(maxPages)` before first open) |
| SQL functions (regexp, uuid, hash, …) | `gosqlite.org/ext/<name>` | `extensions` |
| Migrations / savepoints / scalar reads | `gosqlite.org/sqlitex` | — |

## Per-connection work (hooks, custom functions, vtab registration)

These live on `*sqlite.Conn`, not `*sql.DB`. Pin a connection:

```go
db.SetMaxOpenConns(1)
sc, _ := db.Conn(ctx)
sc.Raw(func(dc any) error {
	c := dc.(*sqlite.Conn)
	return c.RegisterFunc("myfn", func(x int64) int64 { return x*2 }, true)
})
// drive traffic through sc, not db.
```

Always close: `defer db.Close()`. Full narrative: [`docs/`](../../docs/). Gotchas before shipping: the `pitfalls` skill.
