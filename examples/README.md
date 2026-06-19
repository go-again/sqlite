# Examples

Runnable, smoke-tested programs for `gosqlite.org`. They serve two kinds of reader, and the folders are organised around that:

- **Migrating** off another SQLite package (mattn, modernc, glebarez, gorm)? Start in [`migrating/`](migrating/) — your existing code works with a one-line import change.
- **Starting fresh** and want the recommended, modern surface? Start in [`getting-started/`](getting-started/), then dip into [`features/`](features/) for the capability you need.

Run any example by its leaf name or sub-path:

```sh
just example database-sql            # by leaf name
just example features/vfs/crypto     # by sub-path (use this when a leaf is ambiguous)
just examples-list                   # list every runnable example
just examples                        # build + run all of them (smoke test)
```

## Pick your path

| You are… | Go to | What you'll see |
|---|---|---|
| coming from `github.com/mattn/go-sqlite3` | [`migrating/from-mattn`](migrating/from-mattn/) | keep the `sqlite3` driver name; your `_`-prefixed DSN flags are translated; change only the import |
| coming from `modernc.org/sqlite` | [`migrating/from-modernc`](migrating/from-modernc/) | same `sqlite` driver name and DSN — you already work |
| coming from `glebarez/sqlite` or `gorm.io/driver/sqlite` | [`migrating/from-glebarez`](migrating/from-glebarez/) | swap the gorm dialector import; the `sqlite.Open(dsn)` signature is identical |
| new, using `database/sql` | [`getting-started/database-sql`](getting-started/database-sql/) then [`getting-started/config`](getting-started/config/) | the idiomatic foundation, then the typed `sqlite.Config` entry |
| new, using gorm | [`getting-started/gorm`](getting-started/gorm/) | gorm opened from a typed `sqlite.Config` — no DSN strings |
| after a specific capability | [`features/`](features/) | vector / FTS5 search, custom VFS, the `ext/` catalog, hooks, sessions, … |

## The two dials: "compat" vs "modern" (and why raw SQL is neither)

"Modernizing" is not "stop writing SQL." Plain `database/sql` with hand-written queries is the idiomatic Go foundation and stays fully supported. The real choice is **two independent dials**:

**Dial 1 — how you connect:**

| | looks like | when |
|---|---|---|
| compat | `sql.Open("sqlite3", "file:app.db?_pragma=busy_timeout(5000)&_fk=1")` | dropping in for mattn — keep your DSN |
| modern, plain | `sql.Open("sqlite", "file:app.db")` | the standard library way; reach for it any time |
| modern, typed | `sqlite.Open(sqlite.Config{Path: "app.db", Pragmas: …, Encryption: …})` | structured setup — pragmas, encryption, pool tuning in one Go value |

**Dial 2 — how you read/write data:**

| | looks like | when |
|---|---|---|
| raw | `rows, _ := db.Query(...); for rows.Next() { rows.Scan(...) }` | full control; the base every example builds on |
| high-level | `vec.Table` / `fts.Index` typed handles, or gorm models | vector / FTS5 search and ORM ergonomics without hand-written DDL |

An example can be modern on one dial and raw on the other — that's normal. A `hooks` or `session` example uses raw SQL because the *feature* is connection-level; that raw SQL is the right tool, not legacy. The only genuinely "legacy" markers are the `sqlite3` driver alias and the `_`-prefixed DSN flags, and those appear **only** under [`migrating/`](migrating/), where they're the point.

## Migration cheat-sheet

| from | driver name | change you make |
|---|---|---|
| mattn/go-sqlite3 | keep `"sqlite3"` | swap the blank import to `_ "gosqlite.org"`; `_auth*` userauth flags are rejected (dropped upstream) |
| modernc.org/sqlite | keep `"sqlite"` | swap the import; nothing else |
| glebarez/sqlite | n/a (gorm) | `gorm.Open(sqlite.Open(dsn), …)` with `sqlite "gosqlite.org/gorm"` |
| gorm.io/driver/sqlite | n/a (gorm) | same as glebarez — our dialector is a drop-in for both |

When you're ready to modernize, move connection setup from a DSN string to [`sqlite.Config`](../config.go) (see [`getting-started/config`](getting-started/config/)), and reach for the typed [`vec`](../vec/) / [`fts`](../fts/) handles or [gorm](../gorm/) where they save you hand-written SQL. You gain typed pragmas, built-in encryption, pool knobs in one value, typed search, and gorm sidecars — without giving up `database/sql`.

## Folders

- **[`migrating/`](migrating/)** — drop-in compatibility, one example per source package.
- **[`getting-started/`](getting-started/)** — the recommended modern entry points (`database/sql`, typed `Config`, gorm).
- **[`features/`](features/)** — capability reference, sub-grouped:
  - [`search/`](features/search/) — sqlite-vec vector search, FTS5, hybrid rank fusion
  - [`vfs/`](features/vfs/) — custom + built-in virtual file systems (in-memory, encrypted, checksummed, fs.FS-backed)
  - [`extensions/`](features/extensions/) — the loadable `ext/` catalog (scalars, aggregates, vtabs, stores)
  - [`advanced/`](features/advanced/) — hooks, sessions/changesets, backup, page cache, window functions, and more
  - [`gorm/`](features/gorm/) — gorm combined with vec/fts sidecars, encryption, and `ext/` functions
