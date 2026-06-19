---
title: Extensions overview
description: The loadable ext/ catalog — registration patterns for scalar functions, virtual tables, and fs.FS-bound readers.
sidebar:
  order: 18
---

# Extensions

The `ext/` tree adds loadable SQL features as independent Go sub-packages — pick what you need and leave the rest off your import graph. Every `ext/` package is tracked (status / upstream reference / test pin) in [`dev/coverage/ext.md`](../../dev/coverage/ext.md).

Two registration shapes per extension:

```go
// Explicit — register on a specific *sqlite.Conn (the per-conn idiom).
import "gosqlite.org/ext/regexp"
regexp.Register(conn)

// Implicit — blank-import the /auto variant; every conn the driver opens
// picks the extension up via Driver.ConnectHook.
import _ "gosqlite.org/ext/regexp/auto"
```

Beyond that, extensions fall into three usage patterns:

- **[Scalars, aggregates & collations](scalars.md)** — SQL functions you call from any `Where` / `Order` / `Select`. Blank-import `/auto` and go.
- **[Virtual tables](virtual-tables.md)** — data exposed through `CREATE VIRTUAL TABLE … USING module(…)`, some accepting Go data via `sqlite.Pointer`. Includes the `fs.FS`-sandboxed readers (csv, lines, fileio, blobio).
- **[Stores](stores.md)** — persistent specialised stores (Bloom filter, spellfix1 fuzzy vocabulary) that survive `db.Close`.

Several vtab extensions also expose a **typed Go handle** (`csv.Table`, `lines.Table`, `closure.Graph`, `bloom.Filter`, `spellfix1.Vocab`, `rtree.Table`) mirroring `vec.Table` / `fts.Index`, so you can skip the DDL and query SQL. Runnable demos: [`examples/features/extensions/`](../../examples/features/extensions/).

For writing your own SQL functions in Go instead of using a packaged one, see [Window functions & custom UDFs](../guides/window-functions.md).
