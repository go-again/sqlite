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
| `UNINDEXED` columns | ✓ raw | `TestRaw_UnindexedColumn` | FTS5 supports `col UNINDEXED` to skip a column from the index. Not in our typed `Options`. |

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
| `columnsize=0` | ✓ raw | `TestRaw_ColumnsizeZero` | Disables per-column-size tracking; tradeoff for ranking quality. Not in typed `Options`. |

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
| `matchinfo(fts[, flags])` | ✗ | — | FTS3/FTS4 only — not exported by FTS5. Probe confirmed `no such function: matchinfo`. Use `bm25()` or a custom rank function instead. |
| `rank` column on the FTS5 table | ✓ raw | `TestRaw_RankColumn` | Equivalent to `bm25()` with default weights; asserted monotonic in ORDER BY rank. |

## Maintenance commands

Idiomatic FTS5 invocation: `INSERT INTO fts(fts) VALUES('command')`.

| Command | Status | Test |
|---|---|---|
| `'rebuild'` | ✓ typed | `TestExternal_ContentTable`, `TestContentless_*` (rebuild populates external/contentless indexes) |
| `'optimize'` | ✓ typed | `TestIndex_OptimizeAndMerge` |
| `'merge'`, with `rank` set to page count | ✓ typed | `TestIndex_OptimizeAndMerge` |
| `'delete-all'` | ✓ raw | `TestRaw_ContentlessDeleteAll` | Clears contentless table; not in typed API. |
| `'integrity-check'` | ✓ raw | `TestRaw_IntegrityCheck` | Validates internal FTS5 invariants. Useful for diagnosing corruption. |
| `'integrity-check', 1` (full check) | ✓ raw | `TestRaw_IntegrityCheck` | Same test covers both the default and rank-coded forms. |
| `'pgsz'`, `'crisismerge'`, `'automerge'` (tuning knobs) | ✓ raw | `TestRaw_PgszTuning`, `TestRaw_AutomergeAndCrisismerge` | Performance tuning. Raw SQL only. `usermerge` is accepted but not exercised here. |

## Insert / delete

| Operation | Status | Test |
|---|---|---|
| `INSERT INTO fts (rowid, col, ...) VALUES (...)` (single tx) | ✓ typed | `TestNew_DefaultOptions` |
| Batch insert in a single transaction | ✓ typed | covered transitively (Insert wraps in `BeginTx`) |
| `DELETE FROM fts WHERE rowid = ?` | ✓ typed | `TestIndex_Delete`, `TestContentless_DeleteSupported` |
| `UPDATE fts SET col = ? WHERE rowid = ?` | ✓ raw | `TestRaw_UpdateRow` | Works via raw SQL; no typed `Update`. |

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
- **`matchinfo()` is not an FTS5 function.** It existed in FTS3/FTS4 and
  is intentionally not carried over; `bm25()` (or a custom rank
  function) replaces it. Confirmed via probe: SQLite returns
  `no such function: matchinfo`.
- **No `integrity-check` typed wrapper.** Reachable via raw SQL
  (`TestRaw_IntegrityCheck`); add a typed wrapper when there's a real
  use case.
- **No `delete-all` typed wrapper for contentless tables.** Reachable
  via raw SQL (`TestRaw_ContentlessDeleteAll`).
- **No typed Update.** FTS5 supports `UPDATE` on rowid
  (`TestRaw_UpdateRow`); we don't surface it. Delete+Insert in a
  transaction is the supported typed pattern today.

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

## See also

- [`coverage-gorm.md`](coverage-gorm.md) — covers the tag-driven
  `fts/gorm` bridge that wraps `fts.Index` for gorm models, including
  the external / in-table / contentless modes.
- [`../examples/fts-search/`](../examples/fts-search/) — raw `fts.Index`.
- [`../examples/gorm-fts-tagged/`](../examples/gorm-fts-tagged/) — the
  `ftsgorm.Plugin()` flow with a struct-tag-driven FTS5 table.
