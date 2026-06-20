---
title: Limits & pitfalls
description: Bind-value size cap, per-connection virtual tables and hooks, and other behaviours to know.
sidebar:
  order: 25
---

# Limits & pitfalls

- **Bind values are capped at ~2 GiB.** A single bound BLOB/TEXT parameter can't exceed `INT_MAX` bytes — a SQLite limit, surfaced as an error rather than a silent truncation.
- **Virtual tables are per-connection.** A `CREATE VIRTUAL TABLE … USING module(...)` and the module registration live on one connection. Under `database/sql`'s pool, register the module on every conn (the `/auto` blank-import or a `Driver.ConnectHook`), or pin a single connection.
- **Per-conn hooks need a pinned conn.** Update / authorizer / commit / rollback / pre-update / trace hooks are per-connection; `db.Exec`/`db.Query` may pick any pooled conn. Pin with `db.SetMaxOpenConns(1)` + `db.Conn(ctx)` + `sc.Raw(...)` and drive traffic through that conn. See [Hooks](../guides/hooks.md).
- **sqlite-vec quirks** — no `INSERT OR REPLACE` (use `Update`); metadata/partition columns reject NULL; `bit[N]` needs `N%8==0`. See [Vector search](../guides/vector-search.md).
- **libc version pin** — your downstream `go.mod` must use the same `modernc.org/libc` version this module pins, or the C-side ABI drifts. `go mod tidy` against this module maintains it; inspect with `just libc-pin`.
- **userauth is not available** — `_auth*` DSN flags are rejected (the feature was removed upstream).
