# Coverage: blobstore

`gosqlite.org/blobstore` — large, growable, randomly-writable byte objects on top of SQLite incremental BLOB I/O. Built entirely on the public driver surface (`(*sqlite.DB).Conn`, `(*sqlite.Conn).OpenBlob`); no C-ABI or modernc internals. Origin: the `sftp2sqlite` request in `.plans/requirements-sftp2sqlite.md`.

## Status legend

- **✓ typed** — exposed by the `blobstore` package and exercised by a test in
  `blobstore/*_test.go`.
- **✗** — out of scope (see Non-goals).

## API

| Feature | Status | Test | Notes |
|---|---|---|---|
| `Open(db, name, …Option)` + auto-migrate two tables | ✓ typed | `TestRoundTrip`, `TestReattachAcrossStore` | `<name>_objects` + `<name>_chunks`; idempotent (`IF NOT EXISTS`); name validated as a SQL identifier (`TestOpenInvalidName`, `TestValidIdent`). |
| `Create(ctx) → id` | ✓ typed | `TestRoundTrip` | New empty object; chunk size frozen per object. |
| `Size(ctx, id)` | ✓ typed | `TestRoundTrip`, `TestNotFound` | Logical length; `ErrNotFound` for a missing id. |
| `Writer(ctx, id)` → `io.WriterAt`/`io.Closer` | ✓ typed | `TestRoundTrip`, `TestMultiChunkSpan` | Grows on demand; chunks allocated `zeroblob`-full, never grown in place. |
| `Reader(ctx, id)` → `io.ReaderAt`/`io.Closer` | ✓ typed | `TestMultiChunkSpan`, `TestReadAtEOFSemantics` | Clamps to logical size; `io.EOF` past end. |
| Out-of-order writes | ✓ typed | `TestOutOfOrderAndSparseHoles`, `TestConcurrentDistinctObjects` | Any offset, any order. |
| Sparse holes read as zero | ✓ typed | `TestOutOfOrderAndSparseHoles`, `TestGrowPastEnd` | Missing chunk rows in range → zero-fill. |
| `Truncate(ctx, id, size)` — shrink | ✓ typed | `TestTruncateShrinkThenRegrow` | Deletes chunks past the cut; zeroes the boundary-chunk tail so a re-grow reads zeros. |
| `Truncate(ctx, id, size)` — grow (sparse) | ✓ typed | `TestTruncateShrinkThenRegrow` | Bumps logical size only. |
| `Delete(ctx, id)` | ✓ typed | `TestDelete`, `TestNotFound` | Frees object + chunks in one tx; `ErrNotFound` on a missing id. |
| `WithChunkSize(n)` | ✓ typed | `TestMultiChunkSpan`, `TestConcurrentDistinctObjects` | Per-object; default `DefaultChunkSize` (64 KiB). |
| `WithVacuumOnDelete()` | ✓ typed | (option wiring; effect needs incremental auto_vacuum) | Issues `PRAGMA incremental_vacuum` after frees. |
| `ErrNotFound` (`errors.Is`) | ✓ typed | `TestNotFound`, `TestDelete` | Wrapped on every id-not-present path. |
| Concurrent ops on distinct objects | ✓ typed | `TestConcurrentDistinctObjects` | Conn-per-op; writes serialized by `BEGIN IMMEDIATE`. |
| `WithCompression(level)` round-trip (all levels) | ✓ typed | `TestCompressRoundTripAllLevels`, `TestCompressMultiChunkAndSlice` | Per-object compressed mode via `az`; whole-value chunks, not `OpenBlob` (so compressed-mode tests run under `-race`). |
| Compression: sparse / truncate / full-chunk fast path | ✓ typed | `TestCompressSparseHoles`, `TestCompressTruncateShrinkGrow`, `TestCompressFullChunkFastPathAndPartialRMW` | Same invariants as raw mode. |
| Compression: incompressible fallback (no expansion) | ✓ typed | `TestCompressIncompressibleFallback`, `TestEncodeChunkVerbatimFallback` | `chunks.enc=0` (verbatim) when `az` doesn't shrink. |
| Compression: per-object mode + reattach + mixed | ✓ typed | `TestCompressReattachAndMixedModes` | `objects.codec` frozen at Create; any Store reads any object; raw+compressed coexist. |
| Compression: bounded decode (bomb defense) | ✓ typed | `TestDecodeChunkBoundRejectsBomb` | Decode capped at chunk size + 1; rejects over-large frames. |

## Design invariants (asserted by the tests above)

- A chunk is always its object's full chunk size (`zeroblob`-allocated once); growth is new chunk rows, never extending a value — so the `||`/`zeroblob` truncation trap can't arise.
- Logical `size` in `<name>_objects` is authoritative; bytes past it in the live tail chunk are kept zero.
- Each operation borrows one pooled connection and runs its SQL and `OpenBlob` on that same physical connection.
- Compressed objects (`objects.codec=1`) store each chunk as a whole `az`-framed BLOB value (no `OpenBlob`); the chunk's plaintext is still conceptually the full chunk size, so every invariant above holds and only the per-chunk get/put primitive differs. All `az` use is confined to `codec.go`.

## Non-goals

- **A private `OpenInMemory()` backing DB** — conn-per-op needs a shared store (file / `OpenShared` / `MaxOpenConns(1)`). Documented in the package doc.
- **Cross-object or same-object write coordination** — distinct objects never conflict; concurrent writers to one id are last-writer-wins per byte range and the caller's responsibility.
- **Pinned-conn `Writer` fast path** (reuse one `OpenBlob` handle via `Blob.Reopen` across a hot sequential stream) — a possible future option; the current default is conn-per-op for unbounded concurrent handles.
