---
title: DSN flags
description: Every mattn _* DSN flag and the PRAGMA it translates to, plus the URI-level flags and _strict mode.
sidebar:
  order: 23
---

# DSN flag compatibility

Every `_*` DSN flag mattn supported is translated transparently — usually into the equivalent `PRAGMA`. `_strict=1` turns any unknown flag into an error (helpful during migration to flush typos out). The modern alternative to DSN strings is the typed [`sqlite.Config`](../guides/configuration.md).

| Flag (aliases) | Underlying action |
|---|---|
| `_pragma=foo(1)` | `PRAGMA foo=1` (multi-value) |
| `_foreign_keys` / `_fk` | `PRAGMA foreign_keys=` |
| `_busy_timeout` / `_timeout` | `PRAGMA busy_timeout=` |
| `_journal_mode` / `_journal` | `PRAGMA journal_mode=` |
| `_synchronous` / `_sync` | `PRAGMA synchronous=` |
| `_locking_mode` / `_locking` | `PRAGMA locking_mode=` |
| `_secure_delete` | `PRAGMA secure_delete=` |
| `_recursive_triggers` / `_rt` | `PRAGMA recursive_triggers=` |
| `_cache_size` | `PRAGMA cache_size=` |
| `_auto_vacuum` / `_vacuum` | `PRAGMA auto_vacuum=` |
| `_defer_foreign_keys` / `_defer_fk` | `PRAGMA defer_foreign_keys=` |
| `_ignore_check_constraints` | `PRAGMA ignore_check_constraints=` |
| `_case_sensitive_like` / `_cslike` | `PRAGMA case_sensitive_like=` |
| `_query_only` | `PRAGMA query_only=` |
| `_writable_schema` | `PRAGMA writable_schema=` |
| `_loc` | aliased to `_timezone` (auto → Local) |
| `_time_format`, `_time_integer_format`, `_inttotime`, `_texttotime`, `_timezone` | inherited from modernc |
| `_txlock` | sets transaction begin mode |
| `cache`, `mode`, `immutable`, `vfs` | URI-level, passed through |
| `_auth*` | **rejected** — userauth was removed upstream |
| `_strict=1` | unknown flags become hard errors |

## Typed equivalents (`sqlite.Config`)

For new code, prefer the typed [`sqlite.Config`](../guides/configuration.md) over a DSN string — it's compile-checked and self-documenting. The common flags map to fields on `Config.Pragmas` (with typed enum values); anything without a dedicated field goes through `Pragmas.Extra`, which fires `PRAGMA <key> = <value>` exactly like `_pragma=`.

| DSN flag | Typed field | Values |
|---|---|---|
| `_journal` / `_journal_mode` | `Pragmas.JournalMode` | `JournalWAL` · `JournalDelete` · `JournalTruncate` · `JournalPersist` · `JournalMemory` · `JournalOff` |
| `_sync` / `_synchronous` | `Pragmas.Synchronous` | `SynchronousNormal` · `SynchronousOff` · `SynchronousFull` · `SynchronousExtra` |
| `_busy_timeout` / `_timeout` | `Pragmas.BusyTimeout` | a `time.Duration` (mapped to ms) |
| `_fk` / `_foreign_keys` | `Pragmas.ForeignKeys` | `true` / `false` |
| `_cache_size` | `Pragmas.CacheSize` | pages (positive) or KiB (negative) |
| `mode` (URI) | `Config.Mode` | `ModeReadWriteCreate` · `ModeReadWrite` · `ModeReadOnly` · `ModeMemory` |
| `cache` (URI) | `Config.Cache` | `CacheShared` · `CachePrivate` |
| `vfs` (URI) | `Config.VFS` | a registered VFS name |
| *any other* `_pragma=foo(v)` | `Pragmas.Extra` | `map[string]string` → `PRAGMA foo = v` |

Typed-only (no legacy DSN flag): `Pragmas.TempStore` (`TempStoreMemory` · `TempStoreFile` · `TempStoreDefault`) and `Config.Encryption` (transparent at-rest encryption via the [crypto VFS](../guides/encryption.md)). `Config.Pragmas` is applied in struct-declaration order after open; [`RecommendedPragmas()`](../guides/configuration.md) is the production preset (WAL + `busy_timeout=5s` + `foreign_keys=on`). Full field semantics live in the [Configuration guide](../guides/configuration.md) and [pkg.go.dev/gosqlite.org](https://pkg.go.dev/gosqlite.org#Config).
