---
name: vector-search
description: Use when adding semantic, similarity, embedding, or KNN vector search to a Go app with gosqlite — the typed vec.Table API over sqlite-vec, distance metrics, encodings, quantization, and metadata filters.
---

# Vector search (sqlite-vec)

Blank-importing `vec` auto-registers the sqlite-vec extension on every connection.

```go
import (
	_ "gosqlite.org"
	"gosqlite.org/vec"
)

tbl, _ := vec.Create(ctx, db, "docs", 8, vec.Options{Metric: vec.Cosine})
tbl.BatchInsert(ctx, []vec.Row{{Rowid: 1, Embedding: []float32{...}}})

for m, err := range tbl.KNN(ctx, query, 5) { // streaming iter.Seq2
	if err != nil { return err }
	fmt.Println(m.Rowid, m.Distance)
}
hits, _ := tbl.KNNSlice(ctx, query, 5) // non-streaming
```

- **Metrics:** `vec.L2`, `vec.Cosine`, `vec.Dot` (alias L1), `vec.Hamming`.
- **Encodings:** `vec.Options{Encoding: vec.Binary}` or `vec.Int8` / `vec.Bit` for compact storage. int8 needs values in [-1, 1]; `bit[N]` needs `N%8==0`.
- **Metadata filter:** `tbl.KNNSlice(ctx, q, k, vec.WithFilter("lang = ?", "en"))`.
- **String/UUID keys:** `vec.CreateKeyed[string](...)` → `KeyedTable[string]`.
- **Columns:** declare extra columns via `vec.Options{Columns: []vec.Column{{Name: "lang", Type: "text", Kind: vec.Metadata}}}`, then insert with `tbl.InsertRow(ctx, vec.Row{..., Values: map[string]any{"lang": "en"}})`.

## Critical quirks

- **No `INSERT OR REPLACE`** — vec0 rejects it. Use `tbl.Update(...)` to replace a row.
- **Metadata/partition columns reject NULL** (only auxiliary columns allow NULL). Plain `tbl.Insert` only works on tables with no extra columns or aux-only.
- **KNN inside a `gorm.Transaction` is not supported** — run KNN outside the transaction.

For gorm models, use the tag-driven sidecar (`vec:"dim=N;metric=cosine"`) — see the `gorm` skill. To combine with keyword search, see `hybrid-search`. Full reference: [`docs/guides/vector-search.md`](../../docs/guides/vector-search.md).
