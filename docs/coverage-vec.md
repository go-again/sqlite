# Coverage: vec (sqlite-vec)

Last reviewed against `sqlite-vec` as bundled by `modernc.org/sqlite/vec`
(per the `go.mod` pin) on 2026-05-26. The bundled build is what
`vec_version()` reports at runtime; `TestRaw_VecVersion` asserts it
loads.

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

### Lifecycle

| Feature | Status | Test | Notes |
|---|---|---|---|
| `vec.Create(ctx, db, name, dim, opts)` | ✓ typed | `TestTyped_CreateInsertKNN_JSON` | Errors with `ErrAlreadyExists` (wrapped) when the table already exists. |
| `vec.Create(..., vec.WithIfNotExists())` idempotent | ✓ typed | `TestCreate_WithIfNotExists_Succeeds`, `TestCreate_WithIfNotExists_DimMismatchUndetected` | Schema mismatch is NOT validated under WithIfNotExists. |
| `vec.ErrAlreadyExists` sentinel for `errors.Is` | ✓ typed | `TestCreate_DefaultFailsOnSecondCall` |
| `vec.Open` for an existing table | ✓ typed | `TestTyped_Open_OnExistingTable` | Strict schema match (caller asserts dim / metric). |
| Explicit primary key (`id text/integer primary key`) | ✓ typed | `TestKeyed_StringKeys`, `TestKeyed_Int64Keys` | Generic `KeyedTable[K]` (`CreateKeyed[K]` / `OpenKeyed[K]`, `K = int64 \| string`) for UUID/slug keys; `WithKeyColumn` overrides the column name (default `id`). Unblocks the gorm sidecar's non-int64 keys. |

### Column options

| Feature | Status | Test | Notes |
|---|---|---|---|
| `embedding float[N]` (fixed dim float32 column) | ✓ typed | `TestTyped_CreateInsertKNN_JSON`, `TestRaw_SqliteVecSample` | `vec.Create(ctx, db, name, dim, opts)`. |
| `embedding bit[N]` (bit-packed vector) | ✓ typed | `TestTyped_BitEncoding` | `vec.Options{Encoding: vec.Bit}`; quantized via `vec_quantize_binary` at the SQL layer, ranks by Hamming (forced). |
| `embedding int8[N]` (int8-packed vector) | ✓ typed | `TestTyped_Int8Encoding` | `vec.Options{Encoding: vec.Int8}`; quantized via `vec_quantize_int8(?, 'unit')` (assumes [-1, 1] range). 4× smaller than float32. |
| `distance=l1` | ✓ typed | `TestTyped_DotMetric` | Exposed as `vec.Dot` metric (legacy name; semantically L1). |
| `distance=l2` (default) | ✓ typed | `TestTyped_CreateInsertKNN_JSON` | Exposed as `vec.L2`. |
| `distance=cosine` | ✓ typed | `TestTyped_CosineMetric` | Exposed as `vec.Cosine`. |
| `bit[N]` Hamming | ✓ typed | `TestTyped_BitEncoding` | Exposed as `vec.Hamming`; implicit for bit columns (no `distance=` clause emitted). |
| Auxiliary columns (`+col TYPE`) | ✓ typed | `TestTyped_AuxColumnRetrieval` | `vec.Column{Kind: vec.Auxiliary}`; carried via `Row.Values`, read back via `WithSelect` + `KNNSQL`. |
| Metadata columns (`col TYPE`) | ✓ typed | `TestTyped_MetadataPartitionFilter` | `vec.Column{Kind: vec.Metadata}`; filterable in KNN via `WithFilter`. |
| Partition columns | ✓ typed | `TestTyped_MetadataPartitionFilter` | `vec.Column{Kind: vec.Partition}`; partition-key shard, also filterable via `WithFilter`. |
| `chunk_size=N` table option | ✓ typed | `TestTyped_MetadataPartitionFilter` | `Options.ChunkSize`. |
| Per-column `distance_metric=cosine`/`l1` | ✓ raw | `TestRaw_DistanceMetric_Cosine` | Documented vec0 column option; mirrored at the typed level via `Options.Metric`. |

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
| `... WHERE k = ?` (alternate cap syntax) | ✓ raw | `TestRaw_KNN_KEqualsForm` | sqlite-vec accepts both forms. We always emit `LIMIT N` inlined as a literal. |
| Streaming iterator (`iter.Seq2[Neighbor, error]`) | ✓ typed | `TestTyped_KNN_StreamingIteratorBreak` (asserts early-break cleanup) |
| Slice form (`KNNSlice`) | ✓ typed | `TestTyped_CreateInsertKNN_JSON` |
| Filtered KNN via `WithFilter(sql, args...)` | ✓ typed | `TestQuery_WithFilter_RestrictsToRowidSubset`, `TestQuery_WithFilter_EmptyResult`, `TestQuery_WithFilter_InvalidSQLSurfaces` |
| Filter on aux / metadata / partition columns | ✓ raw | `TestRaw_AuxColumn`, `TestRaw_MetadataColumn_FilteredKNN`, `TestRaw_PartitionKey` | Each variant exercised via raw SQL. Reachable through `WithFilter(...)` once you declare the columns. |
| Custom projection / JOIN via `WithSelect` / `WithJoin` / `WithOrderBy` | ✓ typed | `TestKNNSQL_WithSelectJoinFilter`, `TestKNNSQL_WithOrderBy`, `TestKNN_RejectsWithSelect`, `TestKNN_RejectsWithJoin` | `KNN` / `KNNSlice` reject these (row-shape mismatch); use `KNNSQL`. |
| `KNNSQL` returns `(sql, args, err)` for the caller to execute | ✓ typed | `TestKNNSQL_BasicShape` |

### Wire encoding

| Form | Status | Test |
|---|---|---|
| JSON text `[v0, v1, ...]` | ✓ typed | `TestTyped_CreateInsertKNN_JSON` (default) |
| Binary little-endian float32 BLOB (wrapped in `vec_f32(?)` bind) | ✓ typed | `TestTyped_BinaryEncoding` (parity check vs JSON) |
| Int8-quantized (`vec.Int8`; `vec_quantize_int8(?, 'unit')`) | ✓ typed | `TestTyped_Int8Encoding` |
| Bit-quantized (`vec.Bit`; `vec_quantize_binary(?)`, Hamming) | ✓ typed | `TestTyped_BitEncoding` |
| Cross-encoding round-trip | ✓ typed | `TestTyped_BinaryEncoding` asserts ranking matches the JSON baseline. |
| `vec.Encode(v, enc)` raw-bind helper (placeholder + value) | ✓ typed | `TestEncode_JSON_RoundTrip`, `TestEncode_Binary_RoundTrip`, `TestEncode_PlaceholderShape` |

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
| `vec_length(v)` | ✓ raw | `TestRaw_VecLength` | Element count of a json vector. |
| `vec_normalize(v)` | ✓ raw | `TestRaw_VecNormalize` | L2-normalized output; asserted against (3,4)→(0.6,0.8). |
| `vec_slice(v, start, end)` | ✓ raw | `TestRaw_VecSlice` | [start, end) semantics. |
| `vec_to_json(v)` | ✓ raw | `TestRaw_VecToJSON` | Used as a readable inverse for binary helpers in other tests. |
| `vec_quantize_int8(v, mode)` | ✓ raw | `TestRaw_VecQuantizeInt8` | 'unit' range; 1 byte per element. |
| `vec_quantize_binary(v)` | ✓ raw | `TestRaw_VecQuantizeBinary` | 8 sign-bits per byte. |
| `vec_each(v)` (table-valued unpacker) | ✓ raw | `TestRaw_VecEach` | Asserts per-element rows + values. |
| `vec_bit(BLOB)` | ✓ raw | `TestRaw_BitVec_HammingKNN` | For bit-vector construction; used by the Hamming KNN test. |
| `vec_int8(BLOB)` | ✓ raw | `TestRaw_Int8Vec` | int8 vector constructor. |
| `vec_type(v)` | ✓ raw | `TestRaw_VecType` | Returns element-type tag ("float32" / "int8" / ...). |
| `vec_distance_hamming(a, b)` | ✓ raw | covered transitively via `TestRaw_BitVec_HammingKNN` | Used internally for bit-vector ranking; documented helper. |
| `vec_version()` | ✓ raw | `TestRaw_VecVersion` | Canary for extension loading. |
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

- **No typed change of metadata column values after insert.** `Table.Update`
  rewrites only the embedding; to change a metadata/partition/auxiliary value,
  use a raw `UPDATE` or delete-and-reinsert via `InsertRow`.
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

## See also

- [`coverage-gorm.md`](coverage-gorm.md) — covers the tag-driven
  `vec/gorm` bridge that wraps `vec.Table` for gorm models.
- [`../examples/features/search/vec-search/`](../examples/features/search/vec-search/) — raw `vec.Table`.
- [`../examples/features/gorm/vec-tagged/`](../examples/features/gorm/vec-tagged/) — the
  `vecgorm.Plugin()` flow with a struct-tag-driven sidecar.

---

Last reviewed against modernc.org/sqlite/vec on 2026-05-29.
