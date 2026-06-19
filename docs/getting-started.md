---
title: Getting started
description: Install the driver, run your first query, and choose between the "sqlite" and "sqlite3" driver names.
sidebar:
  order: 1
---

# Getting started

## Install

```sh
go get github.com/go-again/sqlite
```

No CGo, no C toolchain, no `apk add`. It builds in `golang:alpine` and distroless images out of the box, and cross-compiles like any pure-Go module.

## Your first query

```go
package main

import (
	"database/sql"
	"log"

	_ "github.com/go-again/sqlite" // registers the "sqlite" and "sqlite3" drivers
)

func main() {
	db, err := sql.Open("sqlite", "file:app.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		log.Fatal(err)
	}
	// ... db.Query / db.Exec / db.BeginTx as usual.
}
```

`db` is a standard `*sql.DB`; everything in `database/sql` works. The runnable version is [`examples/getting-started/database-sql/`](../examples/getting-started/database-sql/main.go).

## Which driver name?

The package registers under **two names**, same singleton driver:

- **`"sqlite"`** — modernc-compatible. Use it for new code.
- **`"sqlite3"`** — mattn-compatible. Use it when migrating mattn code so the rest stays unchanged.

See [Driver names](reference/driver-names.md) for the full story and [Migrating](guides/migrating.md) for per-package recipes.

## A production preset, no DSN string

```go
import sqlite "github.com/go-again/sqlite"

db, _ := sqlite.OpenWAL("app.db") // WAL + busy_timeout=5s + foreign_keys=on
defer db.Close()
// db embeds *sql.DB — every database/sql method works.
```

The four open shortcuts (`OpenInMemory` / `OpenWAL` / `OpenReadOnly` / `OpenShared`), the typed `sqlite.Config`, and typed pragma enums are covered in [Configuration](guides/configuration.md).

## Why CGo-free

Because it transpiles SQLite to Go (via `modernc.org/sqlite`) instead of binding the C library, you get static, dependency-free binaries; trivial cross-compilation (`GOOS=… GOARCH=… go build`); reproducible builds with no system SQLite version skew; and no CGo build flags, C compiler, or `musl`/`glibc` juggling. The trade-off is on hot UDF callback paths — see [Performance](reference/performance.md).

## Next steps

- [Configuration](guides/configuration.md) — structured setup the modern way.
- [Migrating](guides/migrating.md) — coming from another package.
- The [docs home](index.md) lists every guide, the extension catalog, and the reference pages.
