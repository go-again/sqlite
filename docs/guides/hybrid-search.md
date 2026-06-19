---
title: Hybrid search
description: Combine vec.KNN semantic recall with fts.Search lexical precision via Reciprocal Rank Fusion (fusion.RRF).
sidebar:
  order: 7
---

# Hybrid search (semantic + lexical)

When you want the recall of a `vec.KNN` semantic match **and** the precision of an `fts.Index.Search` lexical match, the `fusion` sub-package merges two ranked result sets into one via Reciprocal Rank Fusion (Cormack 2009). No SQL, no extension to load — just Go ranking, no SQLite dependency.

```go
import "github.com/go-again/sqlite/fusion"

vecHits, _ := tbl.KNNSlice(ctx, queryVec, 50)
ftsHits, _ := idx.SearchSlice(ctx, fts.Term("brown fox"), fts.WithLimit(50))

vecKeys := make([]int64, len(vecHits))
for i, h := range vecHits { vecKeys[i] = h.Rowid }
ftsKeys := make([]int64, len(ftsHits))
for i, h := range ftsHits { ftsKeys[i] = h.Key }

top, err := fusion.RRF([][]int64{vecKeys, ftsKeys}, fusion.WithLimit(20))
if err != nil { log.Fatal(err) }
for _, r := range top {
	fmt.Println(r.Key, r.Score)
}
```

`fusion.RRF2(a, b)` is the two-list convenience form. Runnable: [`examples/features/search/fusion-hybrid/`](../../examples/features/search/fusion-hybrid/main.go).

See [Vector search](vector-search.md) and [Full-text search](full-text-search.md) for the two halves.
