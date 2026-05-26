# Coverage: vec (sqlite-vec)

Last reviewed against `sqlite-vec` v0.1.x (bundled via
`modernc.org/sqlite/vec` shipped with `modernc.org/sqlite v1.50.1`) on
2026-05-26.

Underlying SQLite extension docs: https://alexgarcia.xyz/sqlite-vec/

## Status legend

- **✓ typed** — exposed by this module's `vec/` package and exercised by
  a test in `vec/*_test.go`.
- **✓ raw** — works via raw SQL through this driver; a test in
  `vec/raw_test.go` exercises the documented pattern.
- **⚠ inherited** — present in `modernc.org/sqlite/vec`, reachable via raw
  SQL, but not tested in this repository. Use is at your own risk; if it
  breaks against future modernc bumps, we won't see it in CI.
- **✗** — not supported or out of scope.

## vec0 virtual table

The core construct. `CREATE VIRTUAL TABLE name USING vec0(...)`.

### Column options

| Feature | Status | Test | Notes |
|---|---|---|---|
| `embedding float[N]` (fixed dim float32 column) | ✓ typed | `TestTyped_CreateInsertKNN_JSON`, `TestRaw_SqliteVecSample` | `vec.Create(ctx, db, name, dim, opts)`. |
| `embedding bit[N]` (bit-packed vector) | ⚠ inherited | — | Reach via raw SQL. No typed wrapper. |
| `distance=l1` | ✓ typed | `TestTyped_DotMetric` | Exposed as `vec.Dot` metric (legacy name; semantically L1). |
| `distance=l2` (default) | ✓ typed | `TestTyped_CreateInsertKNN_JSON` | Exposed as `vec.L2`. |
| `distance=cosine` | ✓ typed | `TestTyped_CosineMetric` | Exposed as `vec.Cosine`. |
| Auxiliary columns (`+col TYPE`) | ⚠ inherited | — | sqlite-vec supports `+aux_col TYPE` for non-indexed columns. Not in typed API; reach via raw SQL CREATE. |
| Metadata columns (`col TYPE`) | ⚠ inherited | — | Indexed metadata for filtered KNN. Not in typed API. |
| Partition columns | ⚠ inherited | — | sqlite-vec partitions vectors by a discriminator column. Not in typed API. |

### Insert / update / delete

| Operation | Status | Test |
|---|---|---|
| `INSERT INTO t (rowid, embedding) VALUES (?, ?)` (single row) | ✓ typed | `TestTyped_CreateInsertKNN_JSON` |
| Batch insert in a single transaction | ✓ typed | `TestTyped_BatchInsert_OneTx` (asserts exactly 1 commit) |
| `UPDATE t SET embedding = ? WHERE rowid = ?` | ✓ typed | `TestTyped_Update_ChangesEmbedding` |
| `DELETE FROM t WHERE rowid = ?` | ✓ typed | `TestTyped_Delete_RemovesRow` |
| `INSERT OR REPLACE` | ✗ | — | sqlite-vec's vec0 INSERT does not honor conflict resolution. Use `Update` instead. Documented in `CLAUDE.md`. |
| Dim-mismatch validation | ✓ typed | `TestTyped_Insert_DimMismatch` | Client-side check before send. |

### KNN query

| Form | Status | Test |
|---|---|---|
| `... WHERE embedding MATCH ? ORDER BY distance LIMIT N` | ✓ typed | `TestTyped_CreateInsertKNN_JSON` (asserts exact rowids + distances against the upstream sample fixture) |
| `... WHERE k = ?` (alternate cap syntax) | ⚠ inherited | — | sqlite-vec accepts both forms. We always emit `LIMIT N` inlined as a literal. |
| Streaming iterator (`iter.Seq2[Match, error]`) | ✓ typed | `TestTyped_KNN_StreamingIteratorBreak` (asserts early-break cleanup) |
| Slice form (`KNNSlice`) | ✓ typed | `TestTyped_CreateInsertKNN_JSON` |
| Filtered KNN via `WithWhere(sql, args...)` | ✓ typed | `TestQuery_WithWhere_RestrictsToRowidSubset`, `TestQuery_WithWhere_EmptyResult`, `TestQuery_WithWhere_InvalidSQLSurfaces` |
| Filter on aux / metadata / partition columns | ⚠ inherited | — | Possible via `WithWhere(...)` once you declare those columns via raw SQL. Untested in this repo. |

### Wire encoding

| Form | Status | Test |
|---|---|---|
| JSON text `[v0, v1, ...]` | ✓ typed | `TestTyped_CreateInsertKNN_JSON` (default) |
| Binary little-endian float32 BLOB (wrapped in `vec_f32(?)` bind) | ✓ typed | `TestTyped_BinaryEncoding` (parity check vs JSON) |
| Cross-encoding round-trip | ✓ typed | `TestTyped_BinaryEncoding` asserts ranking matches the JSON baseline. |

## SQL helper functions

sqlite-vec exposes a set of scalar SQL functions. These work via raw SQL
through this driver because the extension is auto-registered on every
connection via the `vec.go` blank import.

| Function | Status | Test |
|---|---|---|
| `vec_distance_l2(a, b)` | ✓ typed | `TestL2Distance` (wrapped as Go `L2Distance(ctx, db, a, b)`) |
| `vec_distance_cosine(a, b)` | ✓ typed | `TestCosineDistance` (wrapped as Go `CosineDistance`) |
| `vec_distance_l1(a, b)` | ✓ typed | `TestDotDistance` (wrapped as Go `DotDistance`) |
| `vec_f32(?)` (binary→float32 vec constructor) | ✓ typed | covered transitively by `TestTyped_BinaryEncoding` |
| `vec_length(v)` | ⚠ inherited | — |
| `vec_normalize(v)` | ⚠ inherited | — |
| `vec_slice(v, start, end)` | ⚠ inherited | — |
| `vec_to_json(v)` | ⚠ inherited | — |
| `vec_quantize_int8(v)` | ⚠ inherited | — |
| `vec_quantize_binary(v)` | ⚠ inherited | — |
| `vec_each(v)` (table-valued unpacker) | ⚠ inherited | — |
| `vec_bit(json_array)` | ⚠ inherited | — | For bit-vector construction. |
| Client-side length-mismatch / empty-vector guards on helpers | ✓ typed | `TestDistance_LengthMismatch` |

## Observability

| Feature | Status | Test |
|---|---|---|
| `Wrap(t, WithLogger(slog.Logger))` | ✓ typed | `TestObservable_RecorderAndLogger` |
| `Wrap(t, WithRecorder(Recorder))` | ✓ typed | `TestObservable_RecorderAndLogger`, `TestObservable_KNNErrorRecorded`, `TestObservable_KNNBreakStillFires` |
| No-op wrapping (no options) | ✓ typed | `TestObservable_NoOpWithoutOptions` |
| Recorder receives error on failed op | ✓ typed | `TestObservable_KNNErrorRecorded` |
| Recorder fires exactly once per call (incl. early-break) | ✓ typed | `TestObservable_KNNBreakStillFires` |

## Platform availability

sqlite-vec is transpiled per-target by `modernc.org/sqlite/vec`. Not every
GOOS/GOARCH combination is available upstream. Our CI's `build_all_targets`
job tolerates failures on excluded targets via `|| echo skipping`. We do
not maintain a curated list; treat modernc's release matrix as
authoritative.

## Known gaps worth flagging

- **No typed API for auxiliary / metadata / partition columns.** These are
  sqlite-vec's mechanism for filtering and partitioning. Reachable today
  via raw SQL but no typed wrapper. Adding one is a deliberate API
  expansion; deferred until there's user pressure.
- **No bit-vector typed API.** sqlite-vec supports `bit[N]` vectors with
  Hamming-distance ranking. Reachable via raw SQL; no typed API.
- **No matrix / streaming KNN iteration beyond `iter.Seq2`.** If you need
  to stream millions of rows out of a single query, the iterator handles
  it but you cannot pre-emptively cancel partway through except by
  breaking the range loop.

## Verification recipe

```sh
just test ./vec/...
just example vec-search
```

When `modernc.org/sqlite` bumps and `modernc.org/sqlite/vec` follows,
re-run the recipe and watch for changes in `TestRaw_SqliteVecSample`'s
distance assertions. Those assertions reproduce the upstream documentation
fixture verbatim; a delta there means upstream changed numeric output.
