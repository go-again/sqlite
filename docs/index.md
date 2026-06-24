---
title: gosqlite documentation
description: Guides, feature reference, and the extension catalog for the CGo-free SQLite driver + ecosystem for Go.
sidebar:
  order: 0
---

# Documentation

`gosqlite.org` is a CGo-free SQLite **driver + ecosystem** for Go — a drop-in replacement for `mattn/go-sqlite3` and `modernc.org/sqlite`, with first-class typed APIs for vector search, full-text search, encryption at rest, in-memory MVCC, hybrid ranking, a user-implementable VFS, a bounded page cache, and a catalog of loadable Go SQL extensions — all in one project, all pure Go. Encryption (`vfs/crypto`), a [compressed + encrypted container](guides/vault.md) (`vfs/vault`), blob storage (`blobstore`), and the `glebarez/sqlite`-compatible gorm dialector (`gosqlite.org/gorm`) are opt-in companion modules in the same repo, released together.

The [README](../README.md) is the high-level landing page. These docs are the deep reference.

> **Declarative models with native search — [LiteORM](https://liteorm.org).** Built on this driver, its SQLite vector / full-text / hybrid search runs directly on `vec` / `fts` / `fusion`, and encryption on `vfs/crypto`. Declare `vec:` / `fts:` indexes, `AutoMigrate`, and search with typed `search.For[T]` helpers. See the [skill](../skills/liteorm/SKILL.md) and the runnable [`examples/liteorm/`](../examples/liteorm/).

## Start here

- **[Getting started](getting-started.md)** — install, your first query, and choosing a driver name.
- **[Migrating](guides/migrating.md)** — drop-in recipes from mattn / modernc / glebarez / gorm-sqlite / ncruces.
- **[Configuration](guides/configuration.md)** — `sqlite.Config`, typed pragmas, open shortcuts.

## Guides

| Area | Pages |
|---|---|
| Search | [Vector search](guides/vector-search.md) · [Full-text search](guides/full-text-search.md) · [Hybrid search](guides/hybrid-search.md) |
| ORMs | [LiteORM](https://liteorm.org) — declarative models with native vector / full-text / hybrid search on this driver · [gorm dialector](guides/gorm.md) · [xorm](guides/migrating.md#with-xorm) — works as-is via `xorm.NewEngine("sqlite3", dsn)` |
| Storage / VFS | [vault container](guides/vault.md) · [Encryption](guides/encryption.md) · [Checksums](guides/checksums.md) · [In-memory & embedded](guides/in-memory.md) · [Custom VFS](guides/custom-vfs.md) · [Page cache](guides/page-cache.md) · [Blob storage](guides/blobstore.md) · [Compressed databases](guides/compression.md) |
| Driver capabilities | [Hooks & introspection](guides/hooks.md) · [Sessions / changesets](guides/sessions.md) · [Window functions & UDFs](guides/window-functions.md) · [sqlitex helpers](guides/sqlitex.md) · [Observability](guides/observability.md) |

## Extensions

The loadable `ext/` catalog: [overview](extensions/index.md) · [scalars & aggregates](extensions/scalars.md) · [virtual tables](extensions/virtual-tables.md) · [stores](extensions/stores.md).

## Reference

[Driver names](reference/driver-names.md) · [DSN flags](reference/dsn-flags.md) · [Build tags](reference/build-tags.md) · [Limits](reference/limits.md) · [Performance](reference/performance.md) · [Supported Go](reference/supported-go.md) · [SQLite version](reference/sqlite-version.md)

The Go API reference (every type and function) is on [pkg.go.dev](https://pkg.go.dev/gosqlite.org). AI agents integrating the package should look at the [`skills/`](../skills/) folder.
