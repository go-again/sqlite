---
title: Build tags
description: Mattn's SQLite compile-time build tags and their go-again status — most are no-ops because the features are always on.
sidebar:
  order: 24
---

# Build-tag mapping

Mattn used build tags to enable SQLite compile-time features. Here those features are **already enabled by default** (modernc compiles SQLite with them), so the build tags become no-ops:

| mattn build tag | go-again status |
|---|---|
| `sqlite_fts5` | always on |
| `sqlite_json` (JSON1) | always on |
| `sqlite_math_functions` | always on |
| `sqlite_rtree`, `sqlite_geopoly` | always on |
| `sqlite_dbstat` | always on |
| `sqlite_preupdate_hook` | always on, via `(*Conn).RegisterPreUpdateHook` |
| `sqlite_userauth` | **dropped** (deprecated upstream) |
| `sqlite_unlock_notify` | inherited from modernc |
| `sqlite_vtable` | always on, see [`modernc.org/sqlite/vtab`](https://pkg.go.dev/modernc.org/sqlite/vtab) |

You don't pass any of these tags; just remove them from your build. The SESSION extension is also compiled in (see [Sessions](../guides/sessions.md)).
