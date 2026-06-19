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
