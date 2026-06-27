---
title: Virtual-table extensions
description: Data exposed via CREATE VIRTUAL TABLE — array, lines, csv, statement, closure, pivot, rtree, series — and the fs.FS-bound readers.
sidebar:
  order: 20
---

# Virtual tables

These expose data through `CREATE VIRTUAL TABLE name USING module(...)`; some accept Go data via `sqlite.Pointer(slice)`.

```go
import (
	sqlite "gosqlite.org"
	_ "gosqlite.org/ext/array/auto"
)

// Bind a Go slice as a SQL table:
db.Where(`id IN (SELECT value FROM array(?))`,
	sqlite.Pointer([]int64{1, 5, 17})).Find(&users)

// Or DDL-based vtabs:
db.Exec(`CREATE VIRTUAL TABLE temp.log USING lines(data='INFO ok\nERROR …')`)
db.Exec(`CREATE VIRTUAL TABLE recent USING statement('SELECT name FROM users WHERE id >= ?')`)
```

## Catalog

| Package | What it exposes |
|---|---|
| [`array`](../../ext/array/) | a Go slice as a SQL table (transparent `sqlite.Pointer` path) |
| [`lines`](../../ext/lines/) | split a text blob/file into rows (typed `lines.Table`) |
| [`csv`](../../ext/csv/) | CSV files/strings as tables — delimiters, column affinity, `skip=N` (typed `csv.Table`) |
| [`statement`](../../ext/statement/) | parametrized views with `?N` / `:name` HIDDEN bind columns |
| [`closure`](../../ext/closure/) | `transitive_closure` graph walker with optional depth bounds (typed `closure.Graph`) |
| [`pivot`](../../ext/pivot/) | three-SELECT cross-tab — rows × columns × cell aggregate |
| [`rtree`](../../ext/rtree/) | R-Tree spatial index over the built-in `rtree`/`geopoly` vtabs + custom geometry/query callbacks + a `circle()` geometry (typed `rtree.Table`) |
| [`series`](../../ext/series/) | `generate_series(start, stop[, step])` integer sequence |

Typed handles (`csv.Table`, `lines.Table`, `closure.Graph`, `rtree.Table`) hide the DDL + query SQL. Demos under [`examples/features/extensions/`](../../examples/features/extensions/); end-to-end against gorm: [`gorm/examples/ext-vtabs/`](../../gorm/examples/ext-vtabs/main.go).

## fs.FS-bound readers

`csv`, `lines`, `fileio` accept a sandbox `fs.FS` at registration time (which the `/auto` package can't carry). Install a connect-hook on the singleton driver before `gorm.Open` so every pooled conn picks it up:

```go
sqlite.DefaultDriver().RegisterConnectionHook(
	func(c sqlite.ExecQuerierContext, _ string) error {
		return csv.RegisterFS(c.(*sqlite.Conn), myFsys)
	})
```

The same shape works for `lines.RegisterFS` and `fileio.RegisterFS` (`fileio` provides sandboxed file read/write SQL functions). `blobio` is not filesystem-bound: it registers with plain `blobio.Register(conn)` and provides incremental BLOB I/O over database columns, not an `fs.FS`.
