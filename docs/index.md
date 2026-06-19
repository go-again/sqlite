---
title: go-again/sqlite documentation
description: Guides, feature reference, and the extension catalog for the CGo-free SQLite driver + ecosystem for Go.
sidebar:
  order: 0
---

# Documentation

`github.com/go-again/sqlite` is a CGo-free SQLite **driver + ecosystem** for Go — a drop-in replacement for `mattn/go-sqlite3`, `modernc.org/sqlite`, and the `glebarez/sqlite` gorm dialector, with first-class typed APIs for vector search, full-text search, encryption at rest, in-memory MVCC, hybrid ranking, a user-implementable VFS, a bounded page cache, and a catalog of loadable Go SQL extensions — all in one module, all pure Go.

The [README](../README.md) is the high-level landing page. These docs are the deep reference.

## Start here

- **[Getting started](getting-started.md)** — install, your first query, and choosing a driver name.
- **[Migrating](guides/migrating.md)** — drop-in recipes from mattn / modernc / glebarez / gorm-sqlite / ncruces.
- **[Configuration](guides/configuration.md)** — `sqlite.Config`, typed pragmas, open shortcuts.

## Guides

| Area | Pages |
|---|---|
| Search | [Vector search](guides/vector-search.md) · [Full-text search](guides/full-text-search.md) · [Hybrid search](guides/hybrid-search.md) |
| gorm | [gorm integration + tag-driven sidecars](guides/gorm.md) |
| Storage / VFS | [Encryption](guides/encryption.md) · [Checksums](guides/checksums.md) · [In-memory & embedded](guides/in-memory.md) · [Custom VFS](guides/custom-vfs.md) · [Page cache](guides/page-cache.md) |
| Driver capabilities | [Hooks & introspection](guides/hooks.md) · [Sessions / changesets](guides/sessions.md) · [Window functions & UDFs](guides/window-functions.md) · [sqlitex helpers](guides/sqlitex.md) · [Observability](guides/observability.md) |

## Extensions

The loadable `ext/` catalog: [overview](extensions/index.md) · [scalars & aggregates](extensions/scalars.md) · [virtual tables](extensions/virtual-tables.md) · [stores](extensions/stores.md).

## Reference

[Driver names](reference/driver-names.md) · [DSN flags](reference/dsn-flags.md) · [Build tags](reference/build-tags.md) · [Limits](reference/limits.md) · [Performance](reference/performance.md) · [Supported Go](reference/supported-go.md) · [SQLite version](reference/sqlite-version.md)

The Go API reference (every type and function) is on [pkg.go.dev](https://pkg.go.dev/github.com/go-again/sqlite). AI agents integrating the package should look at the [`skills/`](../skills/) folder.
