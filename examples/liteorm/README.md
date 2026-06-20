# LiteORM + gosqlite

[LiteORM](https://liteorm.org) is an ORM with native vector, full-text, and hybrid search built on gosqlite. gosqlite is its SQLite backend, and the integration is first-class: LiteORM's **vector, full-text, and hybrid (RRF) search** run directly on gosqlite's `vec` / `fts` / `fusion` packages, exposed as declarative model indexes with typed, ranked results.

This example ([`main.go`](main.go)) shows, end to end:

1. **Shared config** — open the backend with the same `gosqlite.Config` the bare driver uses (`sqlite.OpenConfig`).
2. **Declarative search** — a model declares `SearchIndexes()` (full-text + vector); `orm.AutoMigrate` provisions the FTS5 + vec0 sidecars and keeps them in sync; `Repo.Create` writes row and sidecars together.
3. **Typed search** — `search.For[Article](db).Vector / .FullText / .Hybrid` return ranked `[]search.Hit[Article]`.
4. **gosqlite extensions through the ORM** — `REGEXP` (from `gosqlite.org/ext/regexp/auto`) used in LiteORM's typed query builder.
5. **Escape hatch** — `sqlite.Conn(db)` hands back the underlying `*gosqlite.DB` for any driver feature LiteORM doesn't surface.

## Run it

```sh
cd examples/liteorm
go run .
```

## A separate module, building against local `.liteorm`

LiteORM lives in its own repository (`liteorm.org`), so this is an **isolated module** — its `go.mod` keeps LiteORM out of the gosqlite core's dependency graph (the same isolation [`xorm-compat/`](../../xorm-compat/) uses). It builds against the local `.liteorm` reference clone via `replace` directives, so it tracks LiteORM's current typed-search API and resolves `gosqlite.org` to this checkout. Once LiteORM publishes the release carrying the typed `search.For` API, the `replace liteorm.org …` lines can be dropped in favour of a normal `require`.
