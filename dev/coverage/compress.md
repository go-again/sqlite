# Coverage: vfs/compress

`gosqlite.org/vfs/compress` — a SQLite database stored compressed at rest. Phase 0 is a **snapshot** model (not a live VFS): `Open` inflates the compressed file into a private temp working copy, opens it as a normal database, and recompresses it back over the path on `Close` (wired through the root `Config.VFSCloser` seam). Built on the public driver surface (`sqlite.Open`/`Config`) plus `github.com/go-again/az`; all codec contact is confined to `codec.go`. Origin: the compressed-database request; full design in `.plans/plan-compress-vfs.md`.

## Status legend

- **✓ typed** — exposed by the `compress` package and exercised by a test in `vfs/compress/*_test.go`.
- **✗** — out of scope (see Non-goals).

## API

| Feature | Status | Test | Notes |
|---|---|---|---|
| `Open(cfg, opts)` round-trip (create → write → Close → reopen) | ✓ typed | `TestRoundTrip` | On-disk file is compressed (not the SQLite magic); reopen reads the row back. |
| Compression ratio on compressible data | ✓ typed | `TestCompressionRatio` | At-rest file is a small fraction of logical content. |
| Adopt a raw (uncompressed) `.db` | ✓ typed | `TestAdoptRawDatabase` | Header-classified; rewritten compressed on `Close`. |
| Read-only open skips recompress | ✓ typed | `TestReadOnlySkipsRecompress` | `Mode == ModeReadOnly` → at-rest bytes unchanged after `Close`. |
| `Pack(dst, src, level)` / `Unpack(dst, src)` | ✓ typed | `TestPackUnpack` | File transforms without a session; atomic write. |
| Reject in-memory / `cfg.VFS` / empty path | ✓ typed | `TestRejectInMemoryAndVFS` | Same guards as `crypto.Open`. |
| Open failure never clobbers the at-rest file | ✓ typed | `TestOpenFailureLeavesDestUntouched` | Arm-after-success: a failed `sqlite.Open` leaves the recompressor unarmed → no write to dest. |
| Unknown/junk source rejected | ✓ typed | `TestUnknownFormatRejected` | Neither SQLite magic nor a valid frame → error, no clobber. |
| Short (1–3 byte) non-magic source rejected, no clobber | ✓ typed | `TestTinyJunkRejectedNoClobber` | A sub-frame-magic file decompresses to empty with no codec error; `inflate` rejects empty-from-non-empty rather than adopting an empty DB and overwriting the original. |
| Missing destination directory rejected at Open | ✓ typed | `TestMissingDestDirRejected` | Fail fast, not at Close (which many callers ignore). |
| Double `Close` is idempotent | ✓ typed | `TestDoubleCloseIdempotent` | Second `Close` returns nil and leaves the on-disk file unchanged. |
| Empty (0-byte) file treated as fresh | ✓ typed | `TestEmptyFileTreatedAsFresh` | Writable; compressed on Close. |
| WAL session persists through Close→reopen | ✓ typed | `TestWALPersistence` | `consolidate` folds uncheckpointed frames into the main file before compressing. |
| `Level` ladder (`Fastest`…`Best`) + auto-detect decode | ✓ typed | `TestRoundTrip` (Best), `TestPackUnpack` (Better) | `CompressionNone`/zero → default level; decode auto-detects LZ4 vs zstd. |

## Design invariants (asserted by the tests above)

- **No clobber.** The recompressor only writes the at-rest file when `armed` (set after `sqlite.Open` succeeds); the at-rest write is temp-file-then-`rename` (parent dir fsync'd, prior file mode preserved), so the original is replaced atomically and never truncated in place. `inflate` rejects a non-empty source that decompresses to nothing, so a malformed short file is never adopted as an empty database. `Close` is idempotent.
- **Self-contained working file.** `consolidate` runs `PRAGMA wal_checkpoint(TRUNCATE)` before compressing, so `packFile` compresses a single complete `data.db` (sidecar `-wal`/`-shm` are ignored).
- **All `az` use is confined to `codec.go`**, mirroring `blobstore`.

## Non-goals (Phase 0)

- **Live, per-transaction compression** (querying a large DB compressed in place, crash-durable mid-session) — that is a page-translation VFS (a storage engine: directory + free-space allocator + atomic metadata commit), planned separately in `.plans/plan-compress-vfs.md` (Phase 1).
- **Combined compression + encryption that is always both on disk** — also Phase 1/2 (compose the compressing VFS over `vfs/crypto`). Phase 0's working copy is plaintext, so it is not a substitute for at-rest encryption.
- **Compressing an in-use database via `Pack`** — the file must not be open.
