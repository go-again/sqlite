---
title: sqlitex helpers
description: Ergonomic database/sql helpers — savepoints, transaction wrappers, row-callback Execute, scalar reads, and embed.FS migrations.
sidebar:
  order: 16
---

# sqlitex helpers

`sqlitex` is a pure-Go quality-of-life layer over `database/sql` (no lib coupling), named after the zombiezen/crawshaw `sqlitex` lineage.

- **`Save`** — deferred savepoints: `release, _ := sqlitex.Save(ctx, conn); defer release(&err)` commits on nil error, rolls back otherwise.
- **`Transaction` / `ImmediateTransaction`** — run a closure in a transaction that commits on success, rolls back on error.
- **`ExecScript`** — execute a multi-statement SQL script.
- **`Execute`** — a row-callback query with named params and an `ExecOptions{Named, ResultFunc}` struct, plus `ResultStrings` / `ResultInts` collectors — no `rows.Next/Scan/Err/Close` boilerplate.
- **`ResultInt` / `ResultText` / `ResultFloat` / `ResultBool`** — single-scalar reads.
- **`Migrate`** — an `embed.FS` migration runner tracked via `PRAGMA user_version`: applies `NNNN_*.sql` files in order, idempotently.

```go
import "github.com/go-again/sqlite/sqlitex"

//go:embed migrations/*.sql
var migrations embed.FS

n, _ := sqlitex.Migrate(ctx, db, migrations)         // apply pending migrations
_ = sqlitex.Transaction(ctx, db, func(tx *sql.Tx) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO accounts(name) VALUES ('alice')`)
	return e
})
bal, _ := sqlitex.ResultInt(ctx, db, `SELECT balance FROM accounts WHERE name='alice'`)
```

Runnable: [`examples/features/advanced/sqlitex/`](../../examples/features/advanced/sqlitex/main.go).
