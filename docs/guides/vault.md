---
title: Encrypted, compressed container (vfs/vault)
description: vfs/vault stores a SQLite database in a block-structured container where compression and encryption are independent options — plain, compressed, encrypted, or both — with multi-recipient access, masters, signing writers and read-only members, tamper-evidence, an external rollback anchor, membership enumeration, encrypted backups, and online or offline space reclaim.
sidebar:
  order: 8.5
---

# Encrypted, compressed container (`vfs/vault`)

`vfs/vault` stores a SQLite database in a pure-Go, block-structured **container** on disk, where **compression and encryption are independent options**. It is a real storage engine — a file-backed VFS that translates SQLite's page reads and writes to block-aligned slots with a crash-safe copy-on-write commit — and the one place in this project where compression, multi-party encryption, integrity, and rollback resistance come together in a single format. It reuses the same length-preserving page cipher as [`vfs/crypto`](encryption.md); `vfs/crypto` is the lighter single-key page VFS, vault is the container that adds compression, wrapped keys for several recipients, signed administration, and tamper-evidence.

## Four combinations from one format

Compression and encryption are set independently on `vault.Options`, so a database may be plain, compressed, encrypted, or both — the zero value (`Options{}`) is a plain container:

```go
import "gosqlite.org/vfs/vault"

// One vault.Open; the Options pick one of four combinations:
//   vault.Options{}                                          — plain
//   vault.Options{Level: vault.CompressionDefault}           — compressed
//   vault.Options{Key: key}                                  — encrypted
//   vault.Options{Level: vault.CompressionDefault, Key: key} — both (compress then encrypt)
db, _ := vault.Open(sqlite.Config{Path: "app.db"}, vault.Options{Level: vault.CompressionDefault, Key: key})
defer db.Close() // db is a normal *sql.DB
```

Set `Options.Level` to compress (compress **then** encrypt, so the on-disk bytes are both); set `Options.Key` or `Options.Recipients` to encrypt; set both for both. The returned handle is an ordinary database — query, exec, transactions, a connection pool.

## Storage modes

How the database is held on disk is a separate axis from compression and encryption. The [Compressed databases](compression.md) guide is the detailed treatment of the storage modes and the compression level ladder; in brief:

- **`vault.Open` (live, in place)** — the file stays in container form the whole time it is open and is queried in place, durable **per transaction**, with a connection pool and optional WAL. Nothing is ever written to disk in a weaker form than configured (never uncompressed when compression is on, never plaintext when a key is set). Reach for this for a long-lived database that must survive a crash mid-session.
- **`vault.OpenSnapshot` (inflate for the session)** — inflates the file into a private working copy, opens that, and repacks it at `Close`. Durable **per session**, and the working copy is **plaintext on disk** for the lifetime of the handle, so it is not a substitute for at-rest encryption. Reach for it for archival, distribution, and open-modify-close tooling. Cap untrusted compressed input with `Options.MaxInflatedSize` to bound a decompression bomb.
- **`vault.Pack` / `vault.Unpack`** — the same compression transform without a session, for shipping or cold storage.

## Encryption at rest

Set `Options.Key` to encrypt with a single raw key — 32 bytes for the default Adiantum cipher, 64 for AES-XTS-256 (`Options.Cipher`). Derive one from a passphrase with [`crypto.DeriveKey`](encryption.md):

```go
key, _ := crypto.DeriveKey(passphrase, salt, crypto.Adiantum)
db, _ := vault.Open(sqlite.Config{Path: "app.db"}, vault.Options{Key: key})
```

Each compressed block, the page directory, and the transient journal/WAL are encrypted. Outside authenticated mode (below) the guarantee is **confidentiality at rest only**, like `vfs/crypto`: no integrity tag, so the container checksums catch accidental corruption but not deliberate tampering, and a passive attacker still learns the container geometry and per-page compressed sizes. `vault.OpenSnapshot` does **not** encrypt — use live `vault.Open` with a `Key` for an encrypted database.

### Several recipients, no shared secret

To let several parties open one database, each with their own key and no shared secret, set `Options.Recipients` instead of `Options.Key`: a random data key encrypts the container and is wrapped once per recipient — an SSH key, a passphrase, or a generated X25519 pair (the age model) built with [`crypto/keyring`](https://pkg.go.dev/gosqlite.org/crypto/keyring) — into a keyslot inside the file that any one of them can open. Reopen with `Options.Identities`. `Key` and `Recipients` are mutually exclusive and set at create time only.

```go
alice, aliceID, _ := keyring.GenerateX25519() // or keyring.SSHRecipient / keyring.PassphraseRecipient
bob, bobID, _ := keyring.GenerateX25519()

db, _ := vault.Open(sqlite.Config{Path: "shared.db"}, vault.Options{Recipients: []keyring.Recipient{alice, bob}})
db.Close()
db, _ = vault.Open(sqlite.Config{Path: "shared.db"}, vault.Options{Identities: []keyring.Identity{aliceID}})
```

`keyring.ParseAuthorizedKeys` turns a multi-line `authorized_keys` file into a recipient set in one call (and `ParseAuthorizedMasterKeys` for masters/writers), so a deployment can grant access from a key file directly.

### Masters and writers

By default every recipient is an administrator (any of them can change the membership). To restrict that, pin one or more **masters** with `Options.Masters` (ed25519 keys, plus `Options.SignWith` at create): the keyslot's membership is then signed, and only a master may add or remove recipients and masters. A reader enforces this by pinning the masters it trusts at open — a membership not signed by a trusted master is rejected with `vault.ErrUnauthorized`, and a non-master that tries to administer gets `vault.ErrNotMaster`.

Pin **writers** with `Options.Writers` (ed25519, requires `Masters`) for read-only recipients: every commit is then signed by a writer, so a recipient that is not a writer can read and verify but cannot produce a write others accept. A connection opened with `Options.WriteAs` (one of the writers) may write; without one it is read-only and the VFS refuses writes with `vault.ErrReadOnlyRecipient`.

### Changing and enumerating membership

Change the membership on a **closed** database two ways: `vault.Rewrap` re-wraps the data key to a new recipient set without re-encrypting (O(1), access-list management), and `vault.Rekey` re-encrypts under a fresh data key (O(database size), true cryptographic revocation — a removed party and any rolled-back old keyslot then read nothing, and it is the only way to remove a master).

`vault.Members` lets an **admin (master)** list the full current membership — masters, writers, and read-only members, each with its public key and an optional label. It is master-only: the member list is sealed to the masters inside the keyslot, so writers and read-only members cannot enumerate it. It answers "who has access?", which the age envelope alone cannot for read-only members, so an admin can recompute a set before `Rewrap` or `Rekey`.

## Tamper-evidence and rollback resistance

By default encryption gives confidentiality only — a tampered or rolled-back container still opens. Authenticated mode adds integrity: every commit carries a keyed proof over the committed state and each slot a hash, so a modified, truncated, or partially-rolled-back container fails to open with `vault.ErrTampered`. There are two flavours, by who the attacker is:

- **`Options.Authenticate`** (symmetric) — the root proof is an HMAC keyed from the data key, so any key holder both writes and verifies. It protects against an attacker **without** the key and needs no extra keys (just `Key` or `Recipients`).
- **`Options.Writers`** (ed25519, requires `Masters`) — every commit is signed by a writer, so a holder of the read key who is not a writer cannot forge a write others accept. This is the asymmetric flavour, for read-only recipients.

```go
db, _ := vault.Open(cfg, vault.Options{Recipients: recipients, Authenticate: true}) // multi-recipient + tamper-evident
```

Authenticated mode is tamper-evident; full-rollback resistance is **opt-in**. The signed root binds the commit generation, so a state cannot be renumbered — but an attacker who overwrites the file with a *complete, self-consistent earlier committed image* produces a still-validly-signed container that opens. Supply `Options.Anchor` — a monotonic counter kept **outside** the file (a TPM/keystore counter, or `vault.FileAnchor` on separate storage) — to close that: each commit records its generation, and open rejects a generation below the recorded floor with `vault.ErrRolledBack`.

```go
anchor := vault.FileAnchor("/secure-mount/app.floor") // or a TPM/keystore-backed ReplayAnchor
db, _ := vault.Open(cfg, vault.Options{Key: key, Authenticate: true, Anchor: anchor})
```

The anchor is only as strong as its storage — on the same disk as the database it stops nothing. Without an anchor, `Rekey` is the durable revocation path (a fresh data key, so a rolled-back snapshot can no longer be read).

## Backups and reclaiming space

Under churn the container reuses freed blocks, so the at-rest file plateaus rather than growing, and it does not shrink mid-session by design. Four operations manage space and backups:

- **`vault.Compact`** (offline, physical, densest) — on a **closed** database, rewrites every live page into a fresh, densely-packed file and atomically replaces the original, returning freed blocks to the OS. It preserves the encryption and authenticated mode (pass the same `Options`) and **continues** the commit generation, so an `Options.Anchor` stays valid across compaction. For a shared (recipients) image, pass the full `Recipients` to re-seal under a fresh data key (a key rotation, like `Rekey`), or pass only the read `Identities` and Compact **preserves the existing membership and data key** — so a single holder can compact a shared image without re-listing every recipient. It copies `page_count` physical pages, so to reclaim a large deletion it needs the database vacuumed first; `vault.CompactLogical` avoids that.
- **`vault.CompactLogical`** (offline, logical, O(live)) — on a **closed** database, rebuilds via a SQLite `VACUUM INTO` routed through a fresh container, copying only the **live** b-tree rather than every allocated page, then atomically replaces the original. Reclaiming a large deletion is proportional to what is **kept**, not what was freed, in one pass — no prior `incremental_vacuum` or `VACUUM` needed. It preserves the key and membership (a reclaim, not a rotation: pass `Key`, or just `Identities` for a shared image) and writes only encrypted bytes (the rebuild temp and VACUUM's spill go through the same VFS). The rebuild starts a **fresh** commit generation, so an `Options.Anchor` database must use `Compact` instead (which continues the generation); `CompactLogical` refuses one.
- **`vault.CompactLogicalOnline`** (online, logical, O(live)) — the online counterpart of `CompactLogical`: while the database stays **open**, it reads SQLite's freelist, drops the dead pages' container slots (no page is written, moved, or re-encrypted), and relocates + trims the freed blocks back to the OS. So a large deletion shrinks the **mounted** image in time proportional to the **live** data, with **no `incremental_vacuum`** (which re-encrypts every page it moves, since the cipher tweak is the page number) and no `secure_delete`. Call it quiescent: checkpoint first (so the committed freelist is in the container), and make sure no write is in flight (it errors if the container is mid-transaction). Crash-safe (copy-on-write) and invisible to SQLite.
- **`vault.CompactOnline`** (online, relocating) — relocates free space scattered through the **middle** of the container down into the live region and truncates the freed tail, while the database stays **open**. It is the trim step `CompactLogicalOnline` uses, and is also usable directly after `PRAGMA incremental_vacuum` + a checkpoint. It takes an optional progress callback (cumulative bytes reclaimed per batch) so a long pass shows progress. Relocation is crash-safe and invisible to SQLite; offline `Compact` stays marginally denser.
- **`vault.Trim`** (online, tail-only) — returns trailing free blocks to the OS while the database stays **open** (a cheap truncate, no page relocation). It reclaims space when free blocks have collected at the tail and nothing when the tail is in use; for the scattered middle holes a delete leaves behind, use `CompactLogicalOnline` or `CompactOnline`. `vault.ReclaimableBytes` reports how much any of these would return.
- **`vault.Snapshot`** — writes a consistent, encrypted, compressed copy to a **new** path, optionally re-sealed to a different recipient set, with no plaintext on disk. It is the encrypted analogue of `Pack` for handing someone a point-in-time backup, and it starts a fresh commit generation (a backup is independent of the source's anchor).

Do **not** run a full SQLite `VACUUM` to reclaim space on a vault container: because the container is copy-on-write, VACUUM rebuilds the whole database into fresh slots while the old ones are still referenced until commit, so the file roughly **doubles** rather than shrinking, and the rebuilt data lands at the tail where `Trim` cannot reclaim the now-freed middle. To reclaim a large deletion: **online** (mounted), checkpoint then run `vault.CompactLogicalOnline` — one O(live) pass, no `incremental_vacuum`, no re-encryption; **offline** (closed), run `vault.CompactLogical`. Both are the logical rebuild whose growth is bounded by the live data, so you never need a bare `VACUUM` — neither as a reclaim nor as a prep step.

## Scaling, checkpoints, and concurrency

The page directory is stored in fixed-size **segments** under a small segment index, so a commit re-encodes (and re-encrypts) only the segments whose pages changed, plus the index — the per-commit directory write is **O(changed pages), not O(total pages)**, so it stays flat as the database grows rather than getting more expensive with size. `Options.DirSegmentPages` tunes the segment size at create (smaller bounds the per-commit write further, at the cost of a slightly larger index); it is recorded in the container, so it reads back regardless of the value configured later.

Under WAL this also bounds the **checkpoint**: the fold of the WAL back into the container only re-encodes the touched segments, so a checkpoint holds the write lock for a bounded slice rather than rewriting the whole directory. Checkpoint cadence is SQLite's standard `PRAGMA wal_autocheckpoint` — vault does not override it, so set it lower for more frequent, smaller folds (or drive `PRAGMA wal_checkpoint` yourself), then `vault.Trim` returns any freed tail blocks to the OS while the database stays open. `vault.Checkpoint(db, path)` does the fold and the trim in one call — the "shrink a mounted container" operation.

Concurrency follows WAL semantics: a snapshot read on one connection proceeds against the **last committed state** and does not block on another connection's open writer. A read can stall only for the duration of a checkpoint or commit (the window when the writer holds the container lock), which the segmented directory keeps bounded — so large encrypted images no longer expose a multi-second stall to concurrent readers.

## Composing

Anything built on a `*sqlite.DB` inherits the container transparently — including [`blobstore`](blobstore.md): open the store's database through `vault.Open` and every object, chunk, and block is compressed and encrypted on disk, with multi-recipient access and tamper-evidence under the store (runnable: [`examples/vault-blobstore`](../../examples/vault-blobstore/main.go)). For an encrypted database behind an ORM, use [LiteORM](https://liteorm.org), built on this driver. The single-key page VFS without a container is [`vfs/crypto`](encryption.md).

## Module and reference

`vfs/vault` is a separate module (`gosqlite.org/vfs/vault`) so its codec and crypto dependencies stay out of the core graph — `go get gosqlite.org/vfs/vault`. Full API (every type, option, and function): [pkg.go.dev/gosqlite.org/vfs/vault](https://pkg.go.dev/gosqlite.org/vfs/vault) and the keyring at [pkg.go.dev/gosqlite.org/crypto/keyring](https://pkg.go.dev/gosqlite.org/crypto/keyring). Runnable: [`vfs/vault/example/`](../../vfs/vault/example/main.go) walks the whole matrix (plain → compressed → encrypted → multi-recipient → authenticated → writer-signed → snapshot), and [`examples/vault-blobstore`](../../examples/vault-blobstore/main.go) runs a blobstore over a multi-recipient, compressed, authenticated container.
