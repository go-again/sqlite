---
title: Full-text search
description: Typed FTS5 Index[K, V] — tokenizers (incl. custom Go tokenizers), query builder, BM25 ranking, snippet/highlight, vocab.
sidebar:
  order: 6
---

# Full-text search

The `fts/` sub-package is a typed `Index[K, V]` over FTS5.

```go
import (
	_ "gosqlite.org"
	"gosqlite.org/fts"
)

idx, _ := fts.New[int64, string](ctx, db, "docs", fts.Options{
	Tokenizer: fts.Porter{Base: fts.Unicode61{RemoveDiacritics: 2}},
})
idx.Insert(ctx, fts.Attr[int64, string]{Key: 1, Value: "the quick brown fox"})

matches, _ := idx.SearchSlice(ctx, fts.Term("fox"),
	fts.WithRanking(),
	fts.WithSnippet("value", "[", "]", "…", 8))
```

Runnable: [`examples/features/search/fts-search/`](../../examples/features/search/fts-search/main.go).

## Capabilities

- **Tokenizers:** Porter, Unicode61 (with diacritic folding), Trigram — and **custom Go tokenizers** via `(*Conn).RegisterFTS5Tokenizer`, which can do things the built-ins can't (e.g. split `getUserName` into `user`/`name` — see [`examples/features/search/fts-tokenizer/`](../../examples/features/search/fts-tokenizer/main.go)).
- **Query builder:** compose MATCH expressions in Go (`fts.Term`, AND/OR/NOT, phrase, NEAR, column filters, prefix).
- **Ranking:** BM25 via `WithRanking`.
- **Snippet / highlight:** `WithSnippet` / `WithHighlight`.
- **Content modes:** external-content (default), in-table, or contentless.
- **Vocab:** `fts.Vocab` exposes term frequencies + autocomplete from the FTS5 vocabulary tables — see [`examples/features/search/fts-vocab/`](../../examples/features/search/fts-vocab/main.go).

The full matrix of index options, query operators, search options, auxiliary functions, maintenance commands, and tokenizers is in [`dev/coverage/fts.md`](../../dev/coverage/fts.md). For combining lexical + vector results, see [Hybrid search](hybrid-search.md); for full-text search inside an ORM, see [LiteORM](https://liteorm.org).
