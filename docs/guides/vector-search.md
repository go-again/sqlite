---
title: Vector search
description: Typed sqlite-vec KNN — vec.Table, embeddings, distance metrics, encodings, quantization, and metadata filters.
sidebar:
  order: 5
---

# Vector search

The `vec/` sub-package is a typed `Table` API over [sqlite-vec](https://github.com/asg017/sqlite-vec) (bundled by `modernc.org/sqlite/vec`). Blank-importing `vec` auto-registers the extension on every connection.

```go
import (
	_ "gosqlite.org"
	"gosqlite.org/vec"
)

tbl, _ := vec.Create(ctx, db, "docs", 8, vec.Options{Metric: vec.Cosine})
tbl.BatchInsert(ctx, items)
for m, err := range tbl.KNN(ctx, query, 5) { // streaming iter.Seq2
	if err != nil { return err }
	fmt.Println(m.Rowid, m.Distance)
}
```

`KNNSlice` is the non-streaming form. Runnable: [`examples/features/search/vec-search/`](../../examples/features/search/vec-search/main.go).

## Capabilities

- **Metrics:** L2, Cosine, Dot (an alias for L1), Hamming.
- **Encodings:** JSON, binary, and quantized `int8` / `bit` for compact storage.
- **Columns:** metadata, partition-key, and auxiliary columns, plus `chunk_size` tuning.
- **Filtering:** `WithFilter(predicate, args...)` pushes a non-vector constraint into the KNN query (evaluated alongside MATCH).
- **Keyed tables:** `KeyedTable[K]` keys the search on a string / UUID instead of an int rowid — see [`examples/features/search/vec-keyed/`](../../examples/features/search/vec-keyed/main.go).
- **Escape hatch:** `KNNSQL` returns the generated SQL if you need to compose it by hand.

## Quirks worth knowing

- `INSERT OR REPLACE` is **not** honored by vec0 — use `(*Table).Update` for in-place replacement.
- Metadata / partition columns reject `NULL` (only auxiliary columns allow it).
- `bit[N]` needs `N % 8 == 0`; `chunk_size` must be a multiple of 8.
- The `LIMIT`/`k` is inlined as a literal because sqlite-vec's planner needs it visible.

The full matrix of column options, metrics, encodings, and KNN forms is in [`dev/coverage/vec.md`](../../dev/coverage/vec.md). For combining vector + lexical results, see [Hybrid search](hybrid-search.md). For vector search inside an ORM, see [LiteORM](https://liteorm.org).
