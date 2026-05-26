# Coverage: fts (SQLite FTS5)

Last reviewed against SQLite FTS5 (shipped with SQLite 3.53.1 via
`modernc.org/sqlite v1.50.1`) on 2026-05-26.

Underlying FTS5 docs: https://www.sqlite.org/fts5.html

## Status legend

- **✓ typed** — exposed by this module's `fts/` package and exercised by
  a test in `fts/*_test.go`.
- **✓ raw** — works via raw SQL through this driver; a `raw_test.go` test
  exercises the documented pattern.
- **⚠ inherited** — present in the SQLite engine FTS5 ships with;
  reachable via raw SQL, untested in this repo.
- **✗** — not implemented or out of scope.

## Index creation

`CREATE VIRTUAL TABLE name USING fts5(col1, col2, ..., option=value, ...)`.

### Column declaration

| Feature | Status | Test | Notes |
|---|---|---|---|
| Single visible column (default `value`) | ✓ typed | `TestNew_DefaultOptions` | `fts.New[K, V](ctx, db, name, fts.Options{})`. |
| Multiple visible columns via `Options.Columns` | ✓ typed | `TestIndex_MultiColumn`, `TestSearch_RankingWithColumnWeights` | `Match.Value` holds the first column; `Match.Extras[name]` holds the rest. |
| `UNINDEXED` columns | ⚠ inherited | — | FTS5 supports `col UNINDEXED` to skip a column from the index. Not in our `Options`. |

### Index options

| Option | Status | Test | Notes |
|---|---|---|---|
| `tokenize='unicode61 ...'` | ✓ typed | `TestTokenizer_Unicode61_RemoveDiacritics` | `Unicode61{RemoveDiacritics, Categories, Tokenchars, Separators}`. |
| `tokenize='ascii ...'` | ✓ typed | `TestTokenizer_Ascii` | `Ascii{Tokenchars, Separators}`. |
| `tokenize='porter ...'` | ✓ typed | `TestTokenizer_Porter` | `Porter{Base Tokenizer}` — wraps any base tokenizer. |
| `tokenize='trigram ...'` | ✓ typed | `TestTokenizer_Trigram` | `Trigram{CaseSensitive}`. |
| Custom Go tokenizer | ✗ | — | FTS5's `fts5_tokenizer` C API would need a Go binding we don't have. |
| `prefix='2 3 4'` | ✓ typed | covered transitively via `Options.Prefix` | Pre-computes prefix-match indexes for the listed lengths. |
| `content='source_table'` (external content) | ✓ typed | `TestExternal_ContentTable` | `Options.External{ContentTable, ContentRowid}`. |
| `content_rowid='col'` | ✓ typed | same | Maps the external rowid. |
| `content=''` (contentless) | ✓ typed | `TestContentless_RowidsOnly` | `Options.Contentless = true`. |
| `contentless_delete=1` | ✓ typed | `TestContentless_DeleteSupported` | `Options.ContentlessDelete = true`. Requires SQLite ≥ 3.43. |
| `detail=full` (default) | ✓ typed | — | `Options.Detail = fts.DetailFull` or zero. |
| `detail=column` | ✓ typed | — | `Options.Detail = fts.DetailColumn`. |
| `detail=none` | ✓ typed | — | `Options.Detail = fts.DetailNone`. |
| `columnsize=0` | ⚠ inherited | — | Disables per-column-size tracking; tradeoff for ranking quality. Not in `Options`. |

## Query operators

The MATCH right-hand side. Our query builder lives in `fts/query.go`;
direct string assertions for each builder live in `fts/query_test.go`.

| Operator | Status | Test |
|---|---|---|
| Single token | ✓ typed | `TestBuild_Term`, `TestNew_DefaultOptions` |
| Phrase (adjacent tokens) | ✓ typed | `TestBuild_Phrase`, `TestQuery_Phrase` |
| Prefix (`term*`) | ✓ typed | `TestBuild_Prefix`, `TestQuery_Prefix` |
| `AND` | ✓ typed | `TestBuild_And`, `TestQuery_And` |
| `OR` | ✓ typed | `TestBuild_Or`, `TestQuery_Or` |
| `NOT` (binary) | ✓ typed | `TestBuild_Not_Single`, `TestBuild_Not_MultipleNegatives`, `TestQuery_Not` |
| `NEAR(t1 t2, N)` | ✓ typed | `TestBuild_Near` |
| Column-scoped (`col: query`) | ✓ typed | `TestBuild_Column`, `TestIndex_MultiColumn` |
| Raw passthrough | ✓ typed | `TestBuild_Raw` |
| Adversarial-input quoting (embedded quotes, operator keywords) | ✓ typed | `TestBuild_Term`, `TestBuild_Phrase` |

## Search options

Applied via `Search(ctx, query, opts...)` and `SearchSlice(...)`.

| Option | Status | Test |
|---|---|---|
| `WithLimit(n)` | ✓ typed | `TestSearch_LimitOffset` |
| `WithOffset(n)` | ✓ typed | `TestSearch_LimitOffset` |
| `WithRanking()` (default BM25) | ✓ typed | `TestSearch_WithRanking` |
| `WithRanking(w1, w2, ...)` (per-column weights) | ✓ typed | `TestSearch_RankingWithColumnWeights` |
| `WithSnippet(col, before, after, ellipsis, tokens)` | ✓ typed | `TestSearch_SnippetAndHighlight` |
| `WithHighlight(col, before, after)` | ✓ typed | `TestSearch_SnippetAndHighlight` |
| Streaming iterator (`iter.Seq2`) with early-break | ✓ typed | `TestSearch_StreamingBreak` |

## Auxiliary SQL functions

FTS5 exposes scalar/table-valued functions callable in SELECTs against an
FTS5 table. We surface several via search options; others remain reachable
through raw SQL.

| Function | Status | Test |
|---|---|---|
| `bm25(fts, w1, w2, ...)` | ✓ typed | exposed as `WithRanking(weights...)`; tested in `TestSearch_RankingWithColumnWeights` |
| `snippet(fts, col, before, after, ellipsis, tokens)` | ✓ typed | exposed as `WithSnippet`; tested |
| `highlight(fts, col, before, after)` | ✓ typed | exposed as `WithHighlight`; tested |
| `matchinfo(fts[, flags])` | ⚠ inherited | — | Lower-level than bm25; useful for custom ranking. Raw SQL only. |
| `rank` column on the FTS5 table | ⚠ inherited | — | Equivalent to `bm25()` with default weights; usable in raw SELECT. |

## Maintenance commands

Idiomatic FTS5 invocation: `INSERT INTO fts(fts) VALUES('command')`.

| Command | Status | Test |
|---|---|---|
| `'rebuild'` | ✓ typed | `TestExternal_ContentTable`, `TestContentless_*` (rebuild populates external/contentless indexes) |
| `'optimize'` | ✓ typed | `TestIndex_OptimizeAndMerge` |
| `'merge'`, with `rank` set to page count | ✓ typed | `TestIndex_OptimizeAndMerge` |
| `'delete-all'` | ⚠ inherited | — | Clears contentless table; not in typed API. |
| `'integrity-check'` | ⚠ inherited | — | Validates internal FTS5 invariants. Useful for diagnosing corruption. |
| `'integrity-check', 1` (full check) | ⚠ inherited | — |
| `'pgsz'`, `'crisismerge'`, `'usermerge'`, `'automerge'` (tuning knobs) | ⚠ inherited | — | Performance tuning. Raw SQL only. |

## Insert / delete

| Operation | Status | Test |
|---|---|---|
| `INSERT INTO fts (rowid, col, ...) VALUES (...)` (single tx) | ✓ typed | `TestNew_DefaultOptions` |
| Batch insert in a single transaction | ✓ typed | covered transitively (Insert wraps in `BeginTx`) |
| `DELETE FROM fts WHERE rowid = ?` | ✓ typed | `TestIndex_Delete`, `TestContentless_DeleteSupported` |
| `UPDATE fts SET col = ? WHERE rowid = ?` | ⚠ inherited | — | Works via raw SQL; no typed `Update`. |

## Observability

| Feature | Status | Test |
|---|---|---|
| `Wrap(idx, WithLogger(slog.Logger))` | ✓ typed | `TestObservable_RecorderAndLogger` |
| `Wrap(idx, WithRecorder(Recorder))` | ✓ typed | `TestObservable_RecorderAndLogger`, `TestObservable_SearchErrorRecorded` |
| No-op wrapping | ✓ typed | `TestObservable_NoOpWithoutOptions` |
| Recorder receives error on failed op | ✓ typed | `TestObservable_SearchErrorRecorded` |

## Known gaps worth flagging

- **No custom Go tokenizers.** FTS5's `fts5_tokenizer_v2` C API allows
  user-supplied tokenizers. We don't expose a Go binding. The four
  built-in tokenizers (Unicode61, Ascii, Porter, Trigram) are typed.
- **No `matchinfo()` typed wrapper.** Power-user ranking needs the raw
  binary blob FTS5 returns; unwrapping it client-side is non-trivial.
  Reachable via raw SQL.
- **No `integrity-check` typed wrapper.** Add when there's a real use
  case; today, raw `INSERT INTO fts(fts) VALUES('integrity-check')`
  works.
- **No `delete-all` typed wrapper for contentless tables.** Same.
- **No typed Update.** FTS5 supports `UPDATE` on rowid; we don't surface
  it. Delete+Insert in a transaction is the supported pattern today.

## SQLType constraint

`Index[K, V]` is generic over the key and value types. Both must satisfy
`fts.SQLType`:

```go
type SQLType interface {
    ~string | ~[]byte | ~int | ~int32 | ~int64 | ~uint | ~uint32 | ~uint64 | ~float32 | ~float64
}
```

Pure compile-time constraint; no test needed (the type system enforces it).
If you try `fts.New[struct{}, ...]`, the code doesn't compile.

## Verification recipe

```sh
just test ./fts/...
just example fts-search
```

When SQLite bumps inside modernc, FTS5 features can grow. The
`MAJOR.MINOR` of the SQLite version that shipped a given feature is the
trigger to revisit this matrix; check the
[FTS5 changelog section](https://www.sqlite.org/fts5.html#changes) on each
modernc bump.
