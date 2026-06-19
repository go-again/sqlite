---
title: SQLite version
description: Which SQLite version ships, and why we don't pin or fork SQLite itself.
sidebar:
  order: 28
---

# SQLite version

The SQLite version is **inherited from `modernc.org/sqlite`** — the exact build follows whatever that dependency ships in `go.mod`. We don't pin or fork SQLite itself; the only pin we maintain is on the modernc dependency (and its tightly-coupled `modernc.org/libc`, see [Limits](limits.md)).

Compile-time SQLite features (FTS5, JSON1, math functions, R-Tree/geopoly, dbstat, pre-update hook, SESSION, …) are all enabled in the modernc build — see [Build tags](build-tags.md). To move SQLite versions, bump `modernc.org/sqlite` (`just bump-modernc`); `go mod tidy` carries the matching libc pin.
