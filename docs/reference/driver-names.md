---
title: Driver names
description: The "sqlite" and "sqlite3" driver names, and coexisting with mattn/go-sqlite3 in one binary.
sidebar:
  order: 22
---

# Driver names

The package registers the **same** `*Driver` singleton under two names:

```go
sql.Register("sqlite",  drv) // modernc-compatible
sql.Register("sqlite3", drv) // mattn-compatible
```

- Use **`"sqlite"`** for new code (modernc lineage).
- Use **`"sqlite3"`** when migrating mattn code, so `sql.Open("sqlite3", ...)` and the `_*` DSN flags keep working unchanged.

`RegisterFunction` / `Driver.RegisterConnectionHook` apply once and affect both names — there is no separate state.

## Coexistence with mattn

This package and `mattn/go-sqlite3` both claim `"sqlite3"` by default, so blank-importing both in one binary panics at init. To run both (gradual migration, or a mattn-only compiled `.so` extension), register this one under a custom name and leave `"sqlite3"` to mattn:

```go
import (
	"database/sql"
	_ "github.com/mattn/go-sqlite3" // claims "sqlite3"
	sqlite "github.com/go-again/sqlite"
)

func init() {
	sql.Register("sqlite-pure", &sqlite.SQLiteDriver{})
}
```

Then use `sql.Open("sqlite-pure", dsn)` for routes that should use the pure-Go driver and `sql.Open("sqlite3", dsn)` for mattn routes — the choice is per-`*sql.DB`, no shared state. The blank-import-only pattern (auto-registering under `"sqlite3"`) is incompatible with co-importing mattn; use the named registration above instead. `TestCoexistence_CustomNameAlongsideMattn` in `compat_test.go` is the executable example.
