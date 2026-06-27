---
name: migrating
description: Use when switching an existing Go project to gosqlite from mattn/go-sqlite3, modernc.org/sqlite, glebarez/sqlite, gorm.io/driver/sqlite, or ncruces/go-sqlite3.
---

# Migrating to gosqlite

| From | Do this | Driver name |
|---|---|---|
| `mattn/go-sqlite3` | swap the blank import to `_ "gosqlite.org"` | keep `"sqlite3"` |
| `modernc.org/sqlite` | swap the blank import | keep `"sqlite"` |
| `glebarez/sqlite` / `gorm.io/driver/sqlite` | swap dialector import to `gosqlite.org/gorm` | n/a (gorm) |
| `ncruces/go-sqlite3` | partial — see below | `"sqlite3"` |

## mattn / modernc

One-line import swap. `sql.Open(...)`, the `_*` DSN flags, custom-driver registration (`&sqlite3.SQLiteDriver{...}` → `&sqlite.SQLiteDriver{...}`), `Conn.RegisterFunc`, and error sentinels all work unchanged. **`_auth*` userauth flags are rejected** (the feature was dropped upstream) — remove them.

## glebarez / go-gorm dialector

```go
import sqlite "gosqlite.org/gorm"
db, _ := gorm.Open(sqlite.Open("file:my.db?_pragma=foreign_keys(1)"), &gorm.Config{})
```

`TranslateError` behaves the same (`gorm.ErrDuplicatedKey`, etc.). See the `gorm` skill.

## ncruces/go-sqlite3 (partial)

`database/sql` + URI `_pragma=…` DSNs swap cleanly. **Needs rewriting:** any use of ncruces type names (`sqlite3.Conn`, `sqlite3.Context`, …) → move per-conn work to `db.Conn(ctx)` + `conn.Raw(func(dc any){ c := dc.(*sqlite.Conn); … })`; UDF/hook callsites (`CreateFunction` → `Conn.RegisterFunc`, `UpdateHook` → `RegisterUpdateHook`, etc.); `vfs/readervfs` → `vfs.New`/`vfs.NewReader`; `vfs/adiantum`+`vfs/xts` → `vfs/crypto`.

## Coexisting with mattn in one binary

Both claim `"sqlite3"` — blank-importing both panics at init. Register this one under a custom name: `sql.Register("sqlite-pure", &sqlite.SQLiteDriver{})`, then `sql.Open("sqlite-pure", dsn)`.

Full per-package detail + the DSN-flag table: [`docs/guides/migrating.md`](../../docs/guides/migrating.md), [`docs/reference/dsn-flags.md`](../../docs/reference/dsn-flags.md). Runnable: `examples/migrating/`.
