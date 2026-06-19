---
title: Configuration
description: Open shortcuts, the typed sqlite.Config, and typed pragma enums — structured setup with no DSN-string assembly.
sidebar:
  order: 2
---

# Configuration

The same `sqlite.Config` shape feeds both raw `database/sql` and gorm, so there's no per-layer duplication. Legacy DSN strings keep working — `Config` is an additive, type-safe alternative.

## Open shortcuts

Four single-arg constructors cover the cases most consumers want, each with a symmetric gorm-side helper. None needs a `Config` literal:

| Root | gorm | Equivalent to | Use case |
|---|---|---|---|
| `sqlite.OpenInMemory()` | `sqlitegorm.OpenInMemory()` | `Config{Path: sqlite.InMemory}` | Tests, REPLs, scratch DBs (per-conn private). |
| `sqlite.OpenWAL(path)` | `sqlitegorm.OpenWAL(path)` | `Config{Path: path, Pragmas: RecommendedPragmas()}` | Production preset — WAL + busy_timeout=5s + foreign_keys=on. |
| `sqlite.OpenReadOnly(path)` | `sqlitegorm.OpenReadOnly(path)` | `Config{Path: path, Mode: ModeReadOnly}` | Shipped seed DBs, replica reads. Refuses to create the file if missing; refuses writes. |
| `sqlite.OpenShared(name)` | `sqlitegorm.OpenShared(name)` | `Config{Path: name, Mode: ModeMemory, Cache: CacheShared}` | Multi-conn in-memory tests — every open against the same name sees the same rows. |

```go
import sqlite "gosqlite.org"

db, _ := sqlite.OpenWAL("app.db")
defer db.Close() // db embeds *sql.DB — all database/sql methods work.
```

```go
import (
	"gorm.io/gorm"
	sqlitegorm "gosqlite.org/gorm"
)

db, _ := gorm.Open(sqlitegorm.OpenWAL("app.db"), &gorm.Config{})
```

`sqlite.InMemory` is the typed constant for `":memory:"` if you prefer the DSN form. For richer in-memory isolation than `OpenShared`, see [In-memory & embedded](in-memory.md).

## The typed Config

```go
import sqlite "gosqlite.org"

db, err := sqlite.Open(sqlite.Config{
	Path:    "myapp.db",
	Pragmas: sqlite.RecommendedPragmas(), // WAL + busy_timeout=5s + foreign_keys
	Encryption: &sqlite.Encryption{       // optional
		Key: key,                          // 32 bytes for Adiantum
	},
	MaxOpenConns: 8,
})
if err != nil { /* ... */ }
defer db.Close() // drains *sql.DB AND unregisters any encryption VFS the open registered
```

For gorm, the same `Config` via `sqlitegorm.OpenConfig`:

```go
db, err := sqlitegorm.OpenConfig(sqlite.Config{
	Path:    "myapp.db",
	Pragmas: sqlite.RecommendedPragmas(),
})
defer db.Close()
db.AutoMigrate(&MyModel{}) // *gorm.DB methods, unchanged
```

The legacy DSN entries (`sql.Open("sqlite", "file:...")`, `sqlitegorm.Open(dsn)`, `sqlitegorm.New(Config{DSN: dsn})`) keep working unchanged. Runnable: [`examples/getting-started/config/`](../../examples/getting-started/config/main.go).

## Typed pragma values

The `Pragmas` string-valued fields (`JournalMode`, `Synchronous`, `TempStore`) and `Config.Cache` accept typed string-derived enums — autocomplete-friendly and typo-proof:

```go
db, _ := sqlite.Open(sqlite.Config{
	Path: "app.db",
	Pragmas: sqlite.Pragmas{
		JournalMode: sqlite.JournalWAL,
		Synchronous: sqlite.SynchronousNormal,
		TempStore:   sqlite.TempStoreMemory,
		BusyTimeout: 5 * time.Second,
		ForeignKeys: true,
	},
})
```

The constants live in the root package: `sqlite.JournalWAL` / `JournalDelete` / …, `sqlite.SynchronousNormal` / …, `sqlite.TempStoreMemory` / …, `sqlite.CacheShared` / `CachePrivate`. String literals (`JournalMode: "WAL"`) still compile — the typed forms are an additive type-safety win, not a breaking change.

## See also

- [DSN flags](../reference/dsn-flags.md) — every `_*` flag and what pragma it maps to (for legacy / migration).
- [Migrating](migrating.md) — keep your existing DSN strings while you transition.
