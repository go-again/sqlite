# Coverage: blobstore

`gosqlite.org/blobstore` — large, growable, randomly-writable byte objects on top of SQLite incremental BLOB I/O. Built entirely on the public driver surface (`(*sqlite.DB).Conn`, `(*sqlite.Conn).OpenBlob`); no C-ABI or modernc internals. Origin: the `sftp2sqlite` request in `.plans/requirements-sftp2sqlite.md`.

## Status legend

- **✓ typed** — exposed by the `blobstore` package and exercised by a test in
  `blobstore/*_test.go`.
- **✗** — out of scope (see Non-goals).

## API

| Feature | Status | Test | Notes |
|---|---|---|---|
| `Open(db, name, …Option)` + auto-migrate four tables | ✓ typed | `TestRoundTrip`, `TestReattachAcrossStore` | `<name>_objects` + `<name>_blocks` (refcounted chunk bytes) + `<name>_chunks` (the (obj,seq)→block mapping) + `<name>_versions`, plus a partial unique `<name>_blocks_hash` index; idempotent (`IF NOT EXISTS`); name validated as a SQL identifier (`TestOpenInvalidName`, `TestValidIdent`). |
| `OpenReadOnly(db, name)` + `ErrReadOnly` | ✓ typed | `TestOpenReadOnly`, `TestOpenReadOnlyNotProvisioned` | Reattaches with no DDL (read-only media / snapshot browsing); errors if the store is not provisioned; every mutator returns `ErrReadOnly`; reads behave normally. |
| `Create(ctx) → id` | ✓ typed | `TestRoundTrip` | New empty object; chunk size frozen per object. |
| `Size(ctx, id)` | ✓ typed | `TestRoundTrip`, `TestNotFound` | Logical length; `ErrNotFound` for a missing id. |
| `Writer(ctx, id)` → `io.WriterAt`/`io.Closer` | ✓ typed | `TestRoundTrip`, `TestMultiChunkSpan` | Grows on demand; a raw chunk's block is `zeroblob`-allocated full and written in place, never grown. |
| `Reader(ctx, id)` → `io.ReaderAt`/`io.Closer` | ✓ typed | `TestMultiChunkSpan`, `TestReadAtEOFSemantics` | Clamps to logical size; `io.EOF` past end. |
| Out-of-order writes | ✓ typed | `TestOutOfOrderAndSparseHoles`, `TestConcurrentDistinctObjects` | Any offset, any order. |
| Sparse holes read as zero | ✓ typed | `TestOutOfOrderAndSparseHoles`, `TestGrowPastEnd` | Missing chunk mappings in range → zero-fill. |
| `Truncate(ctx, id, size)` — shrink | ✓ typed | `TestTruncateShrinkThenRegrow` | Releases the dropped chunks' blocks by refcount; zeroes the boundary-chunk tail so a re-grow reads zeros. |
| `Truncate(ctx, id, size)` — grow (sparse) | ✓ typed | `TestTruncateShrinkThenRegrow` | Bumps logical size only. |
| `Delete(ctx, id)` | ✓ typed | `TestDelete`, `TestNotFound` | Frees the blocks the object alone holds (shared blocks survive) and cascades to its versions, in one tx; `ErrNotFound` on a missing id. |
| `Batch(ctx, id, fn)` — many writes in one tx | ✓ typed | `TestBatchRoundTrip`, `TestConcurrentBatch` | All of fn's `WriteAt` calls commit together with one size update; the `io.WriterAt` is bound to one pinned conn/transaction. |
| `Batch` atomic rollback | ✓ typed | `TestBatchAtomicRollback`, `TestBatchNotFound` | fn returning an error (or panicking) rolls back the whole batch — a half-written batch never persists; `ErrNotFound` before fn runs for a missing id. |
| `WriteAtFrom(ctx, id, off, r)` | ✓ typed | `TestWriteAtFrom` | Streams an `io.Reader` into an object in one `Batch`; returns bytes written; a sparse gap before off reads as zero. |
| Refcounted blocks + copy-on-write | ✓ typed | `TestCoWRawIsolation`, `TestCoWPartialRawWrite`, `TestCoWCompressedIsolation`, `TestRefcountFreeOnDelete`, `TestRefcountFreeOnTruncate` | A write to a chunk whose block is shared copies the block first; a private block (`refs==1`) is mutated in place; a block is freed when its last reference goes. |
| `Clone(ctx, srcID) → id` | ✓ typed | `TestCloneSharesAndStat`, `TestCloneNotFound` | O(metadata) copy: duplicates the chunk mapping and bumps block refs, no byte I/O; the two objects diverge copy-on-write. `ErrNotFound` for a missing source. |
| Versions (`NewVersion`/`ListVersions`/`OpenVersion`/`WithLabel`) | ✓ typed | `TestVersioningBasic`, `TestVersioningReadOnlyOpen` | A version is a copy-on-write snapshot (a hidden clone) sharing all blocks with the live object until divergence; `OpenVersion` reads it back immutably; works on a read-only store. |
| Retention (`WithObjectVersioning`/`SetRetention`/`Prune`, `Policy`) | ✓ typed | `TestVersioningKeepN`, `TestVersioningMaxAge`, `TestVersioningPruneFreesBytes` | `KeepVersions` (newest N) and `MaxAge` bounds, enforced by `Prune` and the sweep after each `NewVersion`; pruning a version frees the blocks it alone held. |
| `WithDedup()` content-addressed dedup | ✓ typed | `TestDedupSharesIdenticalContent`, `TestDedupDivergeOnWrite`, `TestDedupRawInPlaceClearsHash`, `TestDedupIntraObjectRefcount` | A full-block write byte-identical to an existing block references it (SHA-256, partial unique `hash` index) instead of copying; an in-place raw mutation clears the hash; refcounts settle by per-block multiplicity. |
| `WithChunkSize(n)` | ✓ typed | `TestMultiChunkSpan`, `TestConcurrentDistinctObjects` | Per-object; default `DefaultChunkSize` (64 KiB). |
| `WithVacuumOnDelete()` | ✓ typed | (option wiring; effect needs incremental auto_vacuum) | Issues `PRAGMA incremental_vacuum` after frees. |
| `ErrNotFound` (`errors.Is`) | ✓ typed | `TestNotFound`, `TestDelete` | Wrapped on every id-not-present path. |
| Concurrent ops on distinct objects | ✓ typed | `TestConcurrentDistinctObjects` | Conn-per-op; writes serialized by `BEGIN IMMEDIATE`. |
| `WithCompression(level)` round-trip (all levels) | ✓ typed | `TestCompressRoundTripAllLevels`, `TestCompressMultiChunkAndSlice` | Per-object compressed mode via `az`; whole-value chunks, not `OpenBlob` (so compressed-mode tests run under `-race`). |
| Compression: sparse / truncate / full-chunk fast path | ✓ typed | `TestCompressSparseHoles`, `TestCompressTruncateShrinkGrow`, `TestCompressFullChunkFastPathAndPartialRMW` | Same invariants as raw mode. |
| Compression: incompressible fallback (no expansion) | ✓ typed | `TestCompressIncompressibleFallback`, `TestEncodeChunkVerbatimFallback` | `blocks.enc=0` (verbatim) when `az` doesn't shrink. |
| Compression: per-object mode + reattach + mixed | ✓ typed | `TestCompressReattachAndMixedModes` | `objects.codec` set at Create, mutable via `SetCompression`; any Store reads any object; raw+compressed coexist. |
| Compression: per-object mode + level override (`WithObjectCompression`) | ✓ typed | `TestObjectCompressionOverride`, `TestObjectCompressionForceCompressedOnRawStore`, `TestObjectCompressionPerLevel` | `CompressionNone` forces a raw object in a compressed-default Store and vice-versa; an explicit level is set in the `objects.level` column and used at write (two objects at different levels store different bytes). Old objects / no override → `level=0` → the writing Store's level (unchanged). |
| Compression: change level or mode (`SetCompression`) | ✓ typed | `TestSetCompressionChangesLevel`, `TestSetCompressionConvertsMode`, `TestSetCompressionConvertIncompressible` | A level change on a compressed object rewrites nothing (mixed-level chunks read fine: a head at Best + a tail at Default). A mode change (raw↔compressed, incl. `CompressionNone`) converts every existing chunk in one transaction; raw→compressed and compressed→raw both round-trip, incompressible chunks fall back to verbatim. |
| Object metadata + at-rest ratio (`Stat`) | ✓ typed | `TestStatRatioAndMetadata`, `TestCloneSharesAndStat` | Returns logical size, stored bytes split into `UniqueBytes` (blocks this object alone references) and `SharedBytes` (shared with a clone/version), the at-rest compression ratio (computed from block sizes — not a maintained column), chunk size, mode, level. |
| Compression: bounded decode (bomb defense) | ✓ typed | `TestDecodeChunkBoundRejectsBomb` | Decode capped at chunk size + 1; rejects over-large frames. |
| Exported `Compress`/`Decompress` (standalone codec) | ✓ typed | `TestCompressDecompressExported` | The store's chunk codec for values kept outside the store (inlined in a row): round-trips and shrinks compressible input, verbatim fallback for `CompressionNone`/incompressible (never grown), and a bounded `Decompress` (bomb guard). |
| Caller-transaction writes (`OnConn`) | ✓ typed | `TestOnConnJoinsTx`, `TestOnConnRollback`, `TestOnConnMultiChunkOneTx` | `Store.OnConn(*sql.Conn)` runs `Create`/`WriteAt`/`Batch`/`WriteAtFrom`/`Truncate`/`Delete`/`ReadAt`/`Size` on a caller-held connection, joining its open transaction (per-connection): content is invisible to other connections until commit, commits atomically with the caller's own rows, rolls back with them, multi-chunk content shares one transaction, and an in-tx read sees uncommitted writes. The `*OnConn` transaction bodies are shared with the pooled methods. |

## Design invariants (asserted by the tests above)

- Chunk bytes live in reference-counted `<name>_blocks` rows; `<name>_chunks` maps (obj, seq) → block. A block at `refs == 1` is private and mutated in place (a raw block via `OpenBlob`, a compressed block via a whole-value rewrite); a block at `refs > 1` is shared (by a clone or a version) and copied before any in-place write; a block is freed when its last reference goes. So two objects share storage without either disturbing the other — the basis for Clone, versions, and dedup.
- Growth is new chunk and block rows, never extending a value — so the `||`/`zeroblob` truncation trap can't arise. A raw block is `zeroblob`-allocated full and written in place.
- Logical `size` in `<name>_objects` is authoritative; bytes past it in the live tail chunk are kept zero.
- Each operation borrows one pooled connection and runs its SQL and `OpenBlob` on that same physical connection.
- Compressed objects (`objects.codec=1`) store the chunk's bytes as a whole `az`-framed value in `blocks.data`/`blocks.enc` (no `OpenBlob`); the plaintext is still conceptually the full chunk size, so every invariant above holds and only the per-chunk get/put primitive differs. All `az` use is confined to `codec.go`.

## Non-goals

- **A private `OpenInMemory()` backing DB** — conn-per-op needs a shared store (file / `OpenShared` / `MaxOpenConns(1)`). Documented in the package doc.
- **Cross-object or same-object write coordination** — distinct objects never conflict; concurrent writers to one id are last-writer-wins per byte range and the caller's responsibility.
- **Pinned-conn `Writer` fast path** (reuse one `OpenBlob` handle via `Blob.Reopen` across a hot sequential stream) — a possible future option; the current default is conn-per-op for unbounded concurrent handles.
