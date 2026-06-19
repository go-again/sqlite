---
name: hybrid-search
description: Use when combining vector (semantic) and full-text (lexical) search results into one ranking in a Go app with go-again/sqlite — Reciprocal Rank Fusion via the fusion package.
---

# Hybrid search (semantic + lexical)

The `fusion` package merges two ranked result sets via Reciprocal Rank Fusion (Cormack 2009). Pure Go, no SQLite dependency.

```go
import "github.com/go-again/sqlite/fusion"

vecHits, _ := tbl.KNNSlice(ctx, queryVec, 50)               // see vector-search
ftsHits, _ := idx.SearchSlice(ctx, fts.Term("brown fox"), fts.WithLimit(50)) // see full-text-search

vecKeys := make([]int64, len(vecHits))
for i, h := range vecHits { vecKeys[i] = h.Rowid }
ftsKeys := make([]int64, len(ftsHits))
for i, h := range ftsHits { ftsKeys[i] = h.Key }

top, _ := fusion.RRF([][]int64{vecKeys, ftsKeys}, fusion.WithLimit(20))
for _, r := range top { fmt.Println(r.Key, r.Score) }
```

- `fusion.RRF([][]K{...}, opts...)` — fuse any number of ranked key lists.
- `fusion.RRF2(a, b)` — convenience for exactly two lists.
- Keys must be the same comparable type across lists (the row identity, e.g. `int64` rowid or `string`).

Run the two searches with a generous limit (50–100) each, fuse, then take the top N. Prerequisites: the `vector-search` and `full-text-search` skills. Full reference: [`docs/guides/hybrid-search.md`](../../docs/guides/hybrid-search.md).
