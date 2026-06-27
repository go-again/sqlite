# Coverage: vfs/vault

`gosqlite.org/vfs/vault` — a SQLite database stored in a block-structured **container** at rest, where **compression and encryption are independent options**: a database may be plain, compressed, encrypted, or both — four combinations from one format. The default (`Options{}`) is a plain container (pages stored raw, no key); set `Options.Level` to compress and `Options.Key`/`Options.Recipients` to encrypt. Two open models: **Live** (`Open`, durable per transaction) — a pure-Go, file-backed `vfs.VFS` whose main database is the container, queried in place; and **Snapshot** (`OpenSnapshot`, durable per session) — inflates the file into a private temp working copy, opens it, and repacks it over the path on `Close` (the snapshot working copy is plaintext, so it is for the no-encryption case only). Built on the public driver surface (`sqlite.Open`/`Config`/`gosqlite.org/vfs`) plus `github.com/go-again/az` (compression) and `gosqlite.org/vfs/crypto` + `gosqlite.org/crypto/keyring` (encryption); all codec contact is confined to `codec.go`. The at-rest superblock magic is `VAULTv01`. Origin and full design: `.plans/plan-vfs-vault.md`.

## Status legend

- **✓ typed** — exposed by the `vault` package and exercised by a test in `vfs/vault/*_test.go`.
- **✗** — out of scope (see Non-goals).

## The matrix (compression × encryption)

| Feature | Status | Test | Notes |
|---|---|---|---|
| All four combinations round-trip (plain / compress-only / encrypt-only / both) | ✓ typed | `TestMatrix` | Table-driven over the 2×2; each writes, closes, reopens, reads back, and `integrity_check == ok`. |
| On-disk codec/enc bytes match the options | ✓ typed | `TestMatrix` | Superblock `codec` byte is raw (0) unless `Level` is set; `enc` byte is 0 unless a key/recipients are set. |
| Encrypted bytes carry no plaintext | ✓ typed | `TestMatrix` | A scan of the at-rest file finds no seeded plaintext when encryption is on. |
| `CompressionNone` (zero value) stores pages raw | ✓ typed | `TestMatrix`, `TestLiveRoundTrip` | Default is no compression; a level must be set to compress. Decoding auto-detects, so raw and compressed pages coexist across reopen. |

## Snapshot / file-transform API

| Feature | Status | Test | Notes |
|---|---|---|---|
| `OpenSnapshot(cfg, opts)` round-trip (create → write → Close → reopen) | ✓ typed | `TestRoundTrip` | At-rest file is the container (not the SQLite magic); reopen reads the row back. |
| Compression ratio on compressible data | ✓ typed | `TestCompressionRatio` | At-rest file is a small fraction of logical content when a level is set. |
| Adopt a raw (uncompressed) `.db` | ✓ typed | `TestAdoptRawDatabase` | Header-classified; repacked on `Close`. |
| Read-only open skips repack | ✓ typed | `TestReadOnlySkipsRecompress` | `Mode == ModeReadOnly` → at-rest bytes unchanged after `Close`. |
| `Pack(dst, src, level)` / `Unpack(dst, src)` | ✓ typed | `TestPackUnpack` | File transforms without a session; atomic write. |
| Reject in-memory / `cfg.VFS` / empty path | ✓ typed | `TestRejectInMemoryAndVFS` | Same guards as `crypto.Open`. |
| Open failure never clobbers the at-rest file | ✓ typed | `TestOpenFailureLeavesDestUntouched` | Arm-after-success: a failed `sqlite.Open` leaves the repacker unarmed → no write to dest. |
| Unknown/junk source rejected | ✓ typed | `TestUnknownFormatRejected` | Neither SQLite magic nor a valid frame → error, no clobber. |
| Short (1–3 byte) non-magic source rejected, no clobber | ✓ typed | `TestTinyJunkRejectedNoClobber` | `inflate` rejects empty-from-non-empty rather than adopting an empty DB and overwriting the original. |
| Missing destination directory rejected at OpenSnapshot | ✓ typed | `TestMissingDestDirRejected` | Fail fast, not at Close. |
| Double `Close` is idempotent | ✓ typed | `TestDoubleCloseIdempotent` | Second `Close` returns nil and leaves the on-disk file unchanged. |
| Empty (0-byte) file treated as fresh | ✓ typed | `TestEmptyFileTreatedAsFresh` | Writable; packed on Close. |
| WAL session persists through Close→reopen | ✓ typed | `TestWALPersistence` | `consolidate` folds uncheckpointed frames into the main file before packing. |
| `Options.MaxInflatedSize` caps inflation (bomb guard) | ✓ typed | `TestMaxInflatedSizeCapsInflation` | Untrusted-input safety: a tiny cap rejects a large inflation and leaves dest untouched; 0 = unlimited. |
| `Level` ladder (`Fastest`…`Best`) + auto-detect decode | ✓ typed | `TestRoundTrip` (Best), `TestPackUnpack` (Better) | Decode auto-detects raw vs LZ4 vs zstd. |

## Live VFS API

| Feature | Status | Test | Notes |
|---|---|---|---|
| `Open(cfg, opts)` round-trip (create → write across txns → Close → reopen) | ✓ typed | `TestLiveRoundTrip` | Reopen reads all rows; `integrity_check == ok`; `page_size` matches the container. |
| At-rest file is the container, not raw SQLite | ✓ typed | `TestLiveRoundTrip` | On-disk bytes begin with the `VAULTv01` superblock magic, never the SQLite magic. |
| Compression at rest when a level is set (logical ≫ physical) | ✓ typed | `TestLiveRoundTrip` | Physical container is a fraction of `page_count*page_size`. |
| Updates + deletes persist (COW slot supersession) | ✓ typed | `TestLiveUpdatesAndDeletesPersist` | Rewrites + deletes across transactions survive reopen; `integrity_check == ok`. |
| Foreign (raw `.db`) file rejected, no clobber | ✓ typed | `TestLiveRejectsForeignFile` | A non-container file has no valid superblock → `Open` errors and the file is left byte-for-byte unchanged. |
| `NewVFS` geometry validation + idempotent `Close` | ✓ typed | `TestNewVFSRejectsBadGeometry` | Non-power-of-two page size and `BlockSize > PageSize` are rejected; `VFS.Close` unregisters and is idempotent. |
| Container format: superblock/directory round-trip + ping-pong + CRC rejection | ✓ typed | `TestSuperblock*`, `TestPickSuperblock*`, `TestDirectory*` | Pure encode/decode; highest-generation valid superblock wins; corruption rejected by CRC. |
| Block allocator: first-fit carve, grow, free + coalesce, reuse | ✓ typed | `TestAllocator*`, `TestBlocksFor` | First-fit from the free list, tail-grow on miss, neighbour-coalescing release, freed-run reuse. |
| Allocator rebuilt from directory on open (self-healing) | ✓ typed | `TestRebuildAllocatorFromDirectory` | No persisted free-map; scanning the committed directory reclaims any crash-orphaned block automatically. |
| Crash at EVERY commit step → consistent reopen (never torn) | ✓ typed | `TestCommitCrashAtEveryStep` | A `crashBacking` drops all writes since the last fsync; reopen yields the previous OR new committed state, never a torn mix. |
| Torn newer superblock → fall back to previous generation | ✓ typed | `TestTornSuperblockFallsBackToPrevGen` | Corrupting the newer superblock's CRC region makes reopen select the older valid generation. |
| Corrupted committed directory rejected | ✓ typed | `TestDirectoryCorruptionRejected` | The superblock's `dirChecksum` catches a corrupted directory at open. |
| End-to-end recovery through `Open` | ✓ typed | `TestLiveRecoversFromCorruptLatestSuperblock` | A real database whose newest superblock is corrupted reopens at the previous committed transaction with `integrity_check == ok`. |
| VACUUM (rewrites every page) | ✓ typed | `TestLiveVacuum` | Mass allocation + supersession; reopen is intact with `integrity_check == ok`. |
| Churn does not grow the file unbounded | ✓ typed | `TestLiveChurnDoesNotGrowUnbounded` | Repeated insert/delete/VACUUM cycles plateau — freed blocks are reused, not leaked. |
| Truncate (shrink then regrow) | ✓ typed | `TestMainFileTruncateShrinkAndGrow` | Shrinking frees slots; regrowth zero-fills the gap; both survive reopen. |
| Sparse pages zero-fill | ✓ typed | `TestMainFileSparsePages` | A page never written reads back as zeros across a reopen. |
| Compression ratio vs raw | ✓ typed | `TestLiveCompressionRatioVsRaw` | At the same page size, the compressed container is far smaller than a raw database on log/JSON rows. |
| Multi-connection: concurrent readers + a writer | ✓ typed | `TestLiveConcurrentReadersAndWriter` | A pool shares one container; readers run concurrently with a writer; durable across reopen; `-race` clean. |
| Multi-connection: writers serialize | ✓ typed | `TestLiveConcurrentWritersSerialize` | Concurrent write transactions on disjoint key ranges all land exactly once via the in-process advisory lock. |
| WAL mode engages + round-trips | ✓ typed | `TestLiveWAL` | `journal_mode=WAL` engages (the VFS implements `vfs.ShmFile`); checkpoint folds into the container main DB; survives reopen. |
| WAL shm shared across pools | ✓ typed | `TestLiveWALSharedReaders` | A second pool opening the same path sees rows the first committed under WAL — the dispatcher-owned shm group is keyed by the canonical path. |
| WAL concurrency (readers never block the writer) | ✓ typed | `TestLiveWALConcurrent` | One writer pool + 4 reader pools; each reader's count climbs monotonically; `-race` clean. |

## Encryption

| Feature | Status | Test | Notes |
|---|---|---|---|
| Encryption at rest (data + directory) | ✓ typed | `TestLiveEncryptionRoundTrip` | `Options.Key` encrypts each stored block + the directory (Adiantum + AES-XTS); round-trips, plaintext absent at rest, no-key/wrong-key rejected. |
| Open-time key validation | ✓ typed | `TestEncryptionCheckEnc` | `superblock.enc` recorded; missing key → `ErrEncrypted`, wrong key → `ErrWrongKey` (directory canary), wrong cipher kind / key-on-plaintext rejected. |
| Auxiliary files (journal/WAL/temp) | ✓ typed | `TestPassFileEncryptRoundTrip`, `TestLiveEncryptionWAL` | `passFile` page-aligned RMW encrypts `-journal`/`-wal`/temp with a per-kind cipher domain; aligned/sub-page/spanning writes round-trip, plaintext absent. |
| Multi-recipient keyslots | ✓ typed | `TestLiveEncryptionRecipients`, `TestSuperblockKeyslot` | `Options.Recipients` wraps a random data key per recipient (SSH/passphrase/X25519 via `crypto/keyring`) into a keyslot; any `Options.Identities` match unwraps it, an unlisted identity or none → `ErrNoIdentity`. |
| Recipient management | ✓ typed | `TestRewrap`, `TestRekey`, `TestKeyMgmtErrors` | `Rewrap` re-wraps the data key to a new recipient set without re-encrypting (O(1)); `Rekey` re-encrypts under a fresh key (O(database), true revocation); both refuse an open or non-recipients database. |
| Master keys (signed membership) | ✓ typed | `TestMasterModel`, `TestRemoveMaster` | `Options.Masters`+`SignWith` pin ed25519 admins; only a master may `Rewrap`/`Rekey` (`ErrNotMaster` otherwise); a reader pinning `Options.Masters` rejects a membership not signed by a trusted master (`ErrUnauthorized`); removing a master via `Rekey` locks it out. |

## Authenticated mode (tamper-evidence)

| Feature | Status | Test | Notes |
|---|---|---|---|
| Symmetric: `Options.Authenticate` round-trip | ✓ typed | `TestSymmetricAuthRoundTrip` | HMAC root keyed by a data-key-derived MAC key; round-trips and `integrity_check == ok`. A reopen with only the key still verifies — the on-disk authenticated flag drives verification. |
| Symmetric: requires encryption | ✓ typed | `TestSymmetricAuthRequiresKey` | `Authenticate` without a key/recipients has no secret to key the MAC → rejected at open. |
| Symmetric: downgrade rejected | ✓ typed | `TestSymmetricAuthDowngradeRejected` | A non-authenticated container cannot be opened as authenticated (a key holder cannot strip integrity). |
| Symmetric: tampered directory → `ErrTampered` | ✓ typed | `TestSymmetricAuthDirectoryTamper` | Flipping a byte in the on-disk directory makes reopen fail (the directory no longer matches the MAC'd `dirHash`). |
| Writer-signed: read-only recipients | ✓ typed | `TestAuthenticatedReadOnly`, `TestAuthenticatedTamperRejected` | `Options.Writers`+`WriteAs` sign each commit (per-slot crypto hash + signed superblock extension); a recipient without a writer identity reads but is refused writes (`ErrReadOnlyRecipient`); the wrong master or a tampered directory/slot is rejected. |
| Writer-signed: downgrade rejected | ✓ typed | `TestAuthDowngradeRejected` | An authenticated container opened by a reader who does not require auth is still verified; stripping the authenticated flag is rejected. |
| Writer-signed: authenticated commit crash-safety | ✓ typed | `TestAuthCommitCrash` | A crash at every commit step of a writer-signed container reopens — signature verifying — to the previous or new committed generation, never torn. |
| Role mismatch across the registry | ✓ typed | `TestRoleMismatchConcurrent` | A read-only recipient opening a path a writer already has open does not inherit the writer's signing role through the shared container registry. |
| Second-opener key validation | ✓ typed | `TestRegistryKeyReuse`, `TestEmptyReadOnlyEncrypted` | A second opener of a shared encrypted container must hold the matching key/identity; an empty read-only open with a key errors. |

## Anti-replay anchor and compaction

| Feature | Status | Test | Notes |
|---|---|---|---|
| External anti-replay anchor rejects rollback | ✓ typed | `TestAnchorRejectsRollback` | `Options.Anchor` records each commit's generation OUTSIDE the file; restoring a complete, validly-signed EARLIER image is rejected (`ErrRolledBack`). |
| Anchor: forward reopen + truncate-to-empty | ✓ typed | `TestAnchorAdvancesAndReopens`, `TestAnchorRejectsTruncateToEmpty` | A forward-only database reopens cleanly; replacing it with an empty file is caught as a rollback to before any commit. |
| Anchor: requires auth; reference `FileAnchor` | ✓ typed | `TestAnchorRequiresAuth`, `TestFileAnchor` | An anchor without `Authenticate`/`Writers` is rejected (the generation would be forgeable); `FileAnchor` round-trips and is monotonic. |
| `Compact` shrinks a churned container | ✓ typed | `TestCompactShrinks` | Offline rewrite into a fresh, densely-packed file; freed-block holes are returned to the OS; reopen is intact (`integrity_check == ok`). |
| `Compact` preserves encryption + auth | ✓ typed | `TestCompactEncryptedAuthenticated` | The compacted file reopens, verifies, and holds no plaintext. |
| `Compact` continues the generation (anchor-safe) | ✓ typed | `TestCompactContinuesGenerationForAnchor` | The compacted file's generation continues past the source, so an anchor stays valid and a pre-compaction image is still rejected. |

## Design invariants (asserted by the tests above)

- **One format, orthogonal transforms.** Each page is stored under two independent choices: a codec (raw or `az`-compressed, `superblock.codec`) and a cipher (none or a `vfs/crypto` cipher, `superblock.enc`). When both are on the order is compress THEN encrypt. `Options{}` selects raw + none — a plain container — so neither transform is mandatory.
- **Crash-safe commit (fault-injection proven).** `Sync` writes the new directory to fresh blocks (COW), fsyncs, writes the *alternate* superblock with `generation+1` and a directory checksum, fsyncs, and only then releases superseded extents. A crash before the second fsync leaves the prior generation authoritative; SQLite's rollback journal recovers the logical transaction. `TestCommitCrashAtEveryStep` injects a crash at every commit op.
- **Multi-connection (rollback-journal or WAL).** Connections opening the same canonical path share one refcounted in-memory `container` (process-global registry), coordinating through SQLite's advisory-lock protocol implemented in-process (`nShared` + single `writer`); a `sync.RWMutex` guards the in-memory structures. The handle implements `vfs.ShmFile` (`ShmGroup` = canonical path) so WAL works in-process. `Open` sets `page_size` = container page size, `mmap_size=0`, a default busy timeout, and a rollback journal by default.
- **Nothing weaker than configured hits disk.** Journals and temp files route to a pass-through `File`; the main DB routes to the page-translating `File`. When the database is encrypted, the pass-through `File` also encrypts the `-journal`/`-wal`/temp page-aligned at rest, each file kind under its own cipher domain, so no plaintext page image ever hits disk.
- **Encryption at rest (optional).** The data key is either the raw `Options.Key` or a random key wrapped per recipient (`Options.Recipients`, via `crypto/keyring`) into a keyslot block referenced by `superblock.keyslotOffset` and recovered with `Options.Identities`. `Rewrap`/`Rekey` change the recipient set on a closed database. A known-plaintext canary at the front of the encrypted directory gives a crisp `ErrWrongKey`. By default encryption is confidentiality at rest only — no integrity tag — matching `vfs/crypto`.
- **Authenticated mode adds integrity (two flavours).** With `Options.Authenticate` the root is an HMAC keyed by a key derived from the data key (`deriveMacKey`), so any key holder can write and verify — integrity against an attacker WITHOUT the key (modification, truncation, or a partial/inconsistent rollback fails to open), no extra keys. With `Options.Writers` the root is an ed25519 signature, so a holder of the read key who is not a writer cannot forge a write others accept (read-only recipients). Either way each slot carries a hash and the directory a `dirHash`, bound by the keyed root over `generation ‖ dirHash`; the authenticated flag lives in the signed state, so it cannot be stripped. The symmetric discriminator is `container.macKey != nil`. It is **tamper-evident**, and **rollback-resistant when `Options.Anchor` is set**: the generation is bound (a state cannot be renumbered), and an external monotonic floor (a TPM/keystore counter, or `FileAnchor` on separate storage) makes open reject a generation below the floor with `ErrRolledBack`. Without an anchor a complete self-consistent earlier committed image is still validly signed and opens; each commit advances the anchor, and `Compact` continues the generation so the floor stays valid across compaction.
- **All `az` use is confined to `codec.go`** (`encodePage`/`decodePage`), mirroring the snapshot path and `blobstore`.

## Non-goals

- **Cross-process sharing** — WAL and the advisory locks coordinate in-process only (multiple connections within one process). Multiple OS processes opening the same file concurrently is out of scope.
- **At-rest protection of the WAL working set** — in WAL mode the transient `-wal` frames are uncompressed until checkpoint (encrypted if a key is set); only the checkpointed main DB is in container form. Rollback-journal mode keeps nothing weaker at rest.
- **At-rest encryption of the snapshot working copy** — live `Open` encrypts the database at rest, but the snapshot `OpenSnapshot` inflates to a plaintext working copy on disk, so it is for the no-encryption case (archival/distribution) only, not a substitute for an encrypted database.
- **Bare SQLite `VACUUM` as a reclaim** — a full `VACUUM` on a vault container roughly *doubles* the file (copy-on-write rebuilds into fresh slots while the old ones stay referenced until commit) and lands the rebuilt data at the tail, so it is not a space-reclaim path. Reclaim instead with the dedicated ops: online (mounted) `CompactLogicalOnline` / `CompactOnline` / `Trim`, or offline (closed) `Compact` / `CompactLogical` — all continue the generation so an `Options.Anchor` stays valid (`ReclaimableBytes` reports the recoverable amount). See the public guide's reclaim section.
- **Packing an in-use database via `Pack`** — the file must not be open.
