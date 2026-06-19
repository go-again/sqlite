---
name: full-text-search
description: Use when adding keyword, full-text, or FTS5/BM25 search to a Go app with go-again/sqlite — the typed fts.Index[K, V] API, tokenizers (incl. custom Go tokenizers), query builder, ranking, snippet/highlight.
---

# Full-text search (FTS5)

```go
import (
	_ "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/fts"
)

idx, _ := fts.New[int64, string](ctx, db, "docs", fts.Options{
	Tokenizer: fts.Porter{Base: fts.Unicode61{RemoveDiacritics: 2}},
})
idx.Insert(ctx, fts.Attr[int64, string]{Key: 1, Value: "the quick brown fox"})

matches, _ := idx.SearchSlice(ctx, fts.Term("fox"),
	fts.WithRanking(),                              // BM25
	fts.WithSnippet("value", "[", "]", "…", 8))
```

- **Index type params:** `fts.New[K, V]` — `K` is the row key type, `V` the stored value type.
- **Tokenizers:** `fts.Porter`, `fts.Unicode61{RemoveDiacritics: 2}`, `fts.Trigram`. For CJK / stemming / synonyms, register a **custom Go tokenizer** via `(*Conn).RegisterFTS5Tokenizer`.
- **Query builder:** `fts.Term`, `fts.And/Or/Not`, phrase, NEAR, prefix, column filters — compose in Go, no raw MATCH strings.
- **Options:** `fts.WithRanking()`, `fts.WithSnippet(...)`, `fts.WithHighlight(...)`, `fts.WithLimit(n)` / `WithOffset(n)`, `fts.WithFilter(sql, args...)`.
- **Content modes:** external-content (default), in-table, or contentless via `fts.Options`.
- **Vocab / autocomplete:** `fts.NewVocab(...)` exposes term frequencies + prefix lookups.

Tokenizers are applied symmetrically to both the indexed document and the query, so a reversible transform is invisible to MATCH — inspect indexed terms via fts5vocab if a custom tokenizer behaves unexpectedly. Tokens need `iStart < iEnd` (no zero-width).

For gorm models, use the tag-driven sidecar (`fts5:"tokenize=porter+unicode61"`) — see the `gorm` skill. To combine with vector search, see `hybrid-search`. Full reference: [`docs/guides/full-text-search.md`](../../docs/guides/full-text-search.md).
