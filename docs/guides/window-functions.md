---
title: Window functions & custom UDFs
description: Register scalar, aggregate, and window functions in Go — the mattn-compatible RegisterFunc family.
sidebar:
  order: 15
---

# Window functions & custom UDFs

Register your own SQL functions in Go, on a `*sqlite.Conn`:

- **Scalar** — `Conn.RegisterFunc(name, fn, pure)` (mattn-compatible; reflection maps Go param/return types). Also `sqlite.RegisterFunction` at the top level, applied to every connection.
- **Aggregate** — `Conn.RegisterAggregator(name, factory, pure)`, where `factory` returns a type with `Step(...)` and `Done()` methods.
- **Window** — `Conn.RegisterWindowFunction(name, nArg, ctor, pure)` — the typed window-function entry.
- **Collation** — `Conn.RegisterCollation(name, cmp)`.

```go
sc, _ := db.Conn(ctx)
sc.Raw(func(dc any) error {
	c := dc.(*sqlite.Conn)
	return c.RegisterFunc("clamp", func(x, lo, hi int64) int64 {
		return min(max(x, lo), hi)
	}, true /* deterministic */)
})
```

Mark a function `pure` (deterministic) so SQLite can cache and optimize it. Runnable window-function demo: [`examples/features/advanced/window-function/`](../../examples/features/advanced/window-function/main.go).

For ready-made SQL functions (regexp, hashes, UUIDs, stats, …) without writing Go, see the [Extensions catalog](../extensions/index.md).
