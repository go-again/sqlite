# Coverage: vfs/compress

`gosqlite.org/vfs/compress` — a SQLite database stored compressed at rest, in two models. **Snapshot** (`OpenSnapshot`, Phase 0): inflates the compressed file into a private temp working copy, opens it as a normal database, and recompresses it back over the path on `Close` (wired through the root `Config.VFSCloser` seam); durable per session. **Live** (`Open`, Phase 1): a pure-Go, file-backed `vfs.VFS` whose main database is a block-structured compressed container queried in place, durable per transaction. Built on the public driver surface (`sqlite.Open`/`Config`/`gosqlite.org/vfs`) plus `github.com/go-again/az`; all codec contact is confined to `codec.go`. Origin: the compressed-database request; full design in `.plans/plan-compress-vfs.md` (+ `.plans/plan-compress-vfs-phase1.md`).

## Status legend

- **✓ typed** — exposed by the `compress` package and exercised by a test in `vfs/compress/*_test.go`.
- **✗** — out of scope (see Non-goals).

## API

| Feature | Status | Test | Notes |
|---|---|---|---|
| `OpenSnapshot(cfg, opts)` round-trip (create → write → Close → reopen) | ✓ typed | `TestRoundTrip` | On-disk file is compressed (not the SQLite magic); reopen reads the row back. |
| Compression ratio on compressible data | ✓ typed | `TestCompressionRatio` | At-rest file is a small fraction of logical content. |
| Adopt a raw (uncompressed) `.db` | ✓ typed | `TestAdoptRawDatabase` | Header-classified; rewritten compressed on `Close`. |
| Read-only open skips recompress | ✓ typed | `TestReadOnlySkipsRecompress` | `Mode == ModeReadOnly` → at-rest bytes unchanged after `Close`. |
| `Pack(dst, src, level)` / `Unpack(dst, src)` | ✓ typed | `TestPackUnpack` | File transforms without a session; atomic write. |
| Reject in-memory / `cfg.VFS` / empty path | ✓ typed | `TestRejectInMemoryAndVFS` | Same guards as `crypto.Open`. |
| Open failure never clobbers the at-rest file | ✓ typed | `TestOpenFailureLeavesDestUntouched` | Arm-after-success: a failed `sqlite.Open` leaves the recompressor unarmed → no write to dest. |
| Unknown/junk source rejected | ✓ typed | `TestUnknownFormatRejected` | Neither SQLite magic nor a valid frame → error, no clobber. |
| Short (1–3 byte) non-magic source rejected, no clobber | ✓ typed | `TestTinyJunkRejectedNoClobber` | A sub-frame-magic file decompresses to empty with no codec error; `inflate` rejects empty-from-non-empty rather than adopting an empty DB and overwriting the original. |
| Missing destination directory rejected at OpenSnapshot | ✓ typed | `TestMissingDestDirRejected` | Fail fast, not at Close (which many callers ignore). |
| Double `Close` is idempotent | ✓ typed | `TestDoubleCloseIdempotent` | Second `Close` returns nil and leaves the on-disk file unchanged. |
| Empty (0-byte) file treated as fresh | ✓ typed | `TestEmptyFileTreatedAsFresh` | Writable; compressed on Close. |
| WAL session persists through Close→reopen | ✓ typed | `TestWALPersistence` | `consolidate` folds uncheckpointed frames into the main file before compressing. |
| `Options.MaxInflatedSize` caps inflation (bomb guard) | ✓ typed | `TestMaxInflatedSizeCapsInflation` | Untrusted-input safety: a tiny cap rejects a large inflation and leaves dest untouched; a generous cap opens normally. 0 = unlimited. |
| `Level` ladder (`Fastest`…`Best`) + auto-detect decode | ✓ typed | `TestRoundTrip` (Best), `TestPackUnpack` (Better) | `CompressionNone`/zero → default level; decode auto-detects LZ4 vs zstd. |

## Live VFS API (Phase 1)

| Feature | Status | Test | Notes |
|---|---|---|---|
| `Open(cfg, opts)` round-trip (create → write across txns → Close → reopen) | ✓ typed | `TestLiveRoundTrip` | DB stays compressed on disk; reopen reads all rows; `integrity_check == ok`; `page_size` matches the container. |
| At-rest file is the container, not raw SQLite | ✓ typed | `TestLiveRoundTrip` | On-disk bytes begin with the `goSQLZv1` superblock magic, never the SQLite magic. |
| Compression at rest (logical ≫ physical) | ✓ typed | `TestLiveRoundTrip` | Physical container is a fraction of `page_count*page_size` (≈41% on repetitive rows at 64 KiB pages). |
| Updates + deletes persist (COW slot supersession) | ✓ typed | `TestLiveUpdatesAndDeletesPersist` | Rewrites + deletes across transactions survive reopen with correct counts; `integrity_check == ok`. |
| Foreign (raw `.db`) file rejected, no clobber | ✓ typed | `TestLiveRejectsForeignFile` | A non-container file has no valid superblock → `Open` errors and the file is left byte-for-byte unchanged. |
| `NewVFS` geometry validation + idempotent `Close` | ✓ typed | `TestNewVFSRejectsBadGeometry` | Non-power-of-two page size and `BlockSize > PageSize` are rejected; `VFS.Close` unregisters and is idempotent. |
| Container format: superblock/directory round-trip + ping-pong + CRC rejection | ✓ typed | `TestSuperblock*`, `TestPickSuperblock*`, `TestDirectory*` | Pure encode/decode; highest-generation valid superblock wins; corruption rejected by CRC. |
| Block allocator: first-fit carve, grow, free + coalesce, reuse | ✓ typed | `TestAllocator*`, `TestBlocksFor` | First-fit from the free list, tail-grow on miss, neighbour-coalescing release, freed-run reuse. |
| Allocator rebuilt from directory on open (self-healing) | ✓ typed | `TestRebuildAllocatorFromDirectory` | No persisted free-map; scanning the committed directory reclaims any crash-orphaned block automatically. |
| Crash at EVERY commit step → consistent reopen (never torn) | ✓ typed | `TestCommitCrashAtEveryStep` | A `crashBacking` drops all writes since the last fsync; injecting a crash at each commit op proves reopen yields the previous OR new committed state, never a torn mix. |
| Torn newer superblock → fall back to previous generation | ✓ typed | `TestTornSuperblockFallsBackToPrevGen` | Corrupting the newer superblock's CRC region makes reopen select the older valid generation. |
| Corrupted committed directory rejected | ✓ typed | `TestDirectoryCorruptionRejected` | The superblock's `dirChecksum` catches a corrupted directory at open rather than handing back garbage page mappings. |
| End-to-end recovery through `Open` | ✓ typed | `TestLiveRecoversFromCorruptLatestSuperblock` | A real database whose newest superblock is corrupted reopens at the previous committed transaction with `integrity_check == ok`. |
| VACUUM (rewrites every page) | ✓ typed | `TestLiveVacuum` | The allocator's hardest workout — mass allocation + supersession; reopen is intact with `integrity_check == ok`. |
| Churn does not grow the file unbounded | ✓ typed | `TestLiveChurnDoesNotGrowUnbounded` | Repeated insert/delete/VACUUM cycles plateau at a constant at-rest size — freed blocks are reused, not leaked. |
| Truncate (shrink then regrow) | ✓ typed | `TestMainFileTruncateShrinkAndGrow` | Shrinking frees slots and reports the smaller logical size; regrowth zero-fills the gap; both survive reopen. |
| Sparse pages zero-fill | ✓ typed | `TestMainFileSparsePages` | A page never written reads back as zeros across a reopen. |
| Compression ratio vs raw | ✓ typed | `TestLiveCompressionRatioVsRaw` | At the same page size, the compressed container is far smaller than a raw database on log/JSON rows (≈9% of raw); write throughput measured by `BenchmarkLiveInsert`/`BenchmarkRawInsert`. |
| Multi-connection: concurrent readers + a writer | ✓ typed | `TestLiveConcurrentReadersAndWriter` | A pool of connections shares one container; readers run concurrently with a writer; final count + `integrity_check` correct; durable across reopen; `-race` clean. |
| Multi-connection: writers serialize | ✓ typed | `TestLiveConcurrentWritersSerialize` | Concurrent write transactions on disjoint key ranges all land exactly once via the in-process advisory lock; `integrity_check == ok`. |
| WAL mode engages + round-trips | ✓ typed | `TestLiveWAL` | `journal_mode=WAL` engages (the VFS implements `vfs.ShmFile`); CRUD + TRUNCATE checkpoint fold into the compressed main DB (at-rest magic is the container); `integrity_check == ok`; survives reopen. |
| WAL shm shared across pools | ✓ typed | `TestLiveWALSharedReaders` | A second pool opening the same path sees rows the first committed under WAL — the dispatcher-owned shm group is keyed by the canonical path. |
| WAL concurrency (readers never block the writer) | ✓ typed | `TestLiveWALConcurrent` | One writer pool + 4 reader pools; each reader's count climbs monotonically, no errors, all writes visible; `-race` clean. |
| Encryption at rest (data + directory) | ✓ typed | `TestLiveEncryptionRoundTrip` | `Options.Key` encrypts each compressed block + the directory (Adiantum + AES-XTS); round-trips, plaintext absent at rest, no-key/wrong-key rejected. |
| Encryption: open-time key validation | ✓ typed | `TestEncryptionCheckEnc` | `superblock.enc` recorded; missing key → `ErrEncrypted`, wrong key → `ErrWrongKey` (directory canary), wrong cipher kind / key-on-plaintext rejected. |
| Encryption: auxiliary files (journal/WAL/temp) | ✓ typed | `TestPassFileEncryptRoundTrip`, `TestLiveEncryptionWAL` | `passFile` page-aligned RMW encrypts `-journal`/`-wal`/temp with a per-kind cipher domain; aligned/sub-page/spanning writes round-trip, plaintext absent; an encrypted WAL round-trips. |
| Encryption: multi-recipient keyslots | ✓ typed | `TestLiveEncryptionRecipients`, `TestSuperblockKeyslot` | `Options.Recipients` wraps a random data key per recipient (SSH/passphrase/X25519 via `crypto/keyring`) into a keyslot; any `Options.Identities` match unwraps it, an unlisted identity or none → error (`ErrNoIdentity`); the superblock keyslot offset round-trips. |
| Encryption: recipient management | ✓ typed | `TestRewrap`, `TestRekey`, `TestKeyMgmtErrors` | `Rewrap` re-wraps the data key to a new recipient set without re-encrypting (O(1)); `Rekey` re-encrypts under a fresh key (O(database), true revocation); both refuse an open or non-recipients database. |
| Encryption: master keys (signed membership) | ✓ typed | `TestMasterModel`, `TestRemoveMaster` | `Options.Masters`+`SignWith` pin ed25519 admins; only a master may `Rewrap`/`Rekey` (`ErrNotMaster` otherwise); a reader pinning `Options.Masters` rejects a membership not signed by a trusted master (`ErrUnauthorized`); removing a master via `Rekey` locks it out (read + admin). |
| Encryption: read-only recipients (authenticated) | ✓ typed | `TestAuthenticatedReadOnly`, `TestAuthenticatedTamperRejected` | `Options.Writers`+`WriteAs` sign each commit (per-slot crypto hash + signed superblock extension); a recipient without a writer identity reads but is refused writes (`ErrReadOnlyRecipient`); the wrong master or a tampered directory/slot is rejected (`ErrUnauthorized`). |
| Encryption: authenticated commit crash-safety | ✓ typed | `TestAuthCommitCrash` | A crash at every commit step of a writer-signed container reopens — signature verifying — to the previous or new committed generation, never torn (the extension CRC makes the ping-pong fall back). |
| Encryption: second-opener key validation | ✓ typed | `TestRegistryKeyReuse`, `TestEmptyReadOnlyEncrypted` | A second opener of a shared encrypted container must hold the matching key/identity (no/wrong key rejected, not silently sharing the cipher); an empty read-only open with a key errors. |

### Live VFS design invariants

- **Crash-safe commit (fault-injection proven).** `Sync` writes the new directory to fresh blocks (COW), fsyncs, writes the *alternate* superblock with `generation+1` and a directory checksum, fsyncs, and only then releases superseded extents. A crash before the second fsync leaves the prior generation authoritative; SQLite's rollback journal recovers the logical transaction. `TestCommitCrashAtEveryStep` injects a crash at every commit op and confirms reopen is always a consistent committed generation.
- **Multi-connection (rollback-journal or WAL).** Connections that open the same canonical path share one refcounted in-memory `container` (process-global registry), so they observe the same committed state with no disk re-read. SQLite's advisory-lock protocol — implemented in-process on the handle (`nShared` + single `writer`, mirroring the reference File) — gives many readers / one writer; a `sync.RWMutex` on the container guards the in-memory structures. The handle also implements `vfs.ShmFile` (`ShmGroup` = canonical path), so WAL works: the dispatcher shares one shm WAL index across the connections. `Open` sets `page_size` = container page size, `mmap_size=0`, a default busy timeout, and a rollback journal by default (caller may opt into WAL).
- **Only the main DB is compressed.** Journals and temp files route to a pass-through `File`; the main DB routes to the page-translating compressing `File`. When the database is encrypted, the pass-through `File` also encrypts the `-journal`/`-wal`/temp page-aligned at rest, so no plaintext page image ever hits disk.
- **Encryption at rest (optional, `Options.Key` or `Options.Recipients`).** Each compressed block and the page directory are encrypted (compress THEN encrypt) per block-aligned extent, reusing the length-preserving cipher of `gosqlite.org/vfs/crypto` (Adiantum default, AES-XTS); the auxiliaries are encrypted by the pass-through file's page-aligned read-modify-write, each file kind under its own cipher domain. The data key is either the raw `Options.Key` or a random key wrapped per recipient (`Options.Recipients`, via `crypto/keyring`) into a keyslot block referenced by `superblock.keyslotOffset` and recovered with `Options.Identities` (none matching → `ErrNoIdentity`); `Rewrap`/`Rekey` change the recipient set on a closed database. A known-plaintext canary at the front of the encrypted directory gives a crisp `ErrWrongKey`; `superblock.enc` records the cipher (`0` = unencrypted, fully back-compatible). Confidentiality at rest only — no integrity tag — matching `vfs/crypto`.
- **All `az` use is confined to `codec.go`** (`encodePage`/`decodePage`), mirroring the snapshot path.

## Design invariants (asserted by the tests above)

- **No clobber.** The recompressor only writes the at-rest file when `armed` (set after `sqlite.Open` succeeds); the at-rest write is temp-file-then-`rename` (parent dir fsync'd, prior file mode preserved), so the original is replaced atomically and never truncated in place. `inflate` rejects a non-empty source that decompresses to nothing, so a malformed short file is never adopted as an empty database. `Close` is idempotent.
- **Self-contained working file.** `consolidate` runs `PRAGMA wal_checkpoint(TRUNCATE)` before compressing, so `packFile` compresses a single complete `data.db` (sidecar `-wal`/`-shm` are ignored).
- **All `az` use is confined to `codec.go`**, mirroring `blobstore`.

## Non-goals

- **Cross-process sharing** — WAL and the advisory locks coordinate in-process only (multiple connections within one process), like the dispatcher's WAL design. Multiple OS processes opening the same compressed file concurrently is out of scope.
- **At-rest compression of the WAL working set** — in WAL mode the transient `-wal` frames are uncompressed until checkpoint; only the checkpointed main DB is compressed. Rollback-journal mode keeps nothing uncompressed at rest.
- **At-rest encryption of the snapshot working copy** — live `Open` encrypts the database at rest (`Options.Key` or `Options.Recipients`), but the snapshot `OpenSnapshot` inflates to a plaintext working copy on disk, so it is not a substitute for an encrypted database.
- **Returning freed space to the OS** — under churn the container reuses freed blocks (so the at-rest file plateaus rather than growing), but it does not shrink the physical file back to the filesystem mid-session; rebuilding the free list on reopen reclaims space for reuse. Returning bytes to the OS is an offline compaction (a future container→container rewrite), not yet implemented.
- **Compressing an in-use database via `Pack`** — the file must not be open.
