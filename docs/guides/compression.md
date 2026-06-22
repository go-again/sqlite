---
title: Compressed databases
description: Store a SQLite database compressed on disk with gosqlite — compress.Open queries it compressed in place (durable per transaction, multi-connection, optional WAL); compress.OpenSnapshot inflates a working copy; plus Pack / Unpack for shipping a compressed .db.
sidebar:
  order: 19
---

# Compressed databases

`vfs/compress` keeps a SQLite database compressed on disk, in two modes. Pick by how the database is used:

- **[Live](#live-compressed-in-place--open) — `compress.Open`:** query the database while it stays compressed on disk, durable per transaction, with a connection pool and optional WAL. Reach for this for a large, compressible database that stays open and must survive a crash mid-session.
- **[Snapshot](#snapshot-inflate-for-the-session--opensnapshot) — `compress.OpenSnapshot`:** inflate the whole database into a private working copy for the session, recompress it at `Close`. Reach for this for archival, distribution, backups, and open-modify-close tooling.

Both are pure Go, ship as a separate module, and share the same level ladder. Live `Open` can also encrypt the database at rest with a key (see [Encryption at rest](#encryption-at-rest)); `OpenSnapshot` does not.

## Live: compressed in place — `Open`

`compress.Open` hands back a normal database handle whose on-disk file stays compressed the entire time it is open. It is a real storage engine — a pure-Go, file-backed VFS that translates SQLite's page reads and writes to compressed, block-aligned slots in a block-structured container — so **nothing is ever written to disk uncompressed**.

```go
import "gosqlite.org/vfs/compress"

db, _ := compress.Open(sqlite.Config{Path: "app.db.az"}, compress.Options{})
defer db.Close()
// use db exactly like *sql.DB — query, exec, transactions, a connection pool
```

- **Durable per transaction.** Each commit atomically flips a ping-pong superblock, so a crash leaves the previous committed state intact and SQLite's rollback journal recovers the rest — this is fault-injection tested at every step of the commit.
- **Multiple connections.** A connection pool is safe: connections that open the same path share one in-memory container and coordinate through the VFS's in-process advisory locks — many readers, one writer at a time.
- **Rollback journal by default; WAL optional.** Set `Pragmas.JournalMode` to WAL to opt in. In WAL mode the main database stays compressed and only the transient `-wal` frames are uncompressed (folded into compressed slots on checkpoint). WAL coordination is in-process — multiple connections within one process.
- **Compressed at rest.** On log/JSON-shaped data at the default large page size, the on-disk container is a small fraction of the logical database.

`compress.Open` refuses to open a file that is not one of its containers rather than risk clobbering it, and rejects a malformed or hostile container with an error instead of trusting it.

It is a good fit for a large, compressible database that must stay open continuously and survive crashes — and a poor fit for hot, random small writes (every page write recompresses).

## Snapshot: inflate for the session — `OpenSnapshot`

`compress.OpenSnapshot` inflates the compressed file into a private working copy, opens that copy as an ordinary database, and recompresses it back over the original path at `Close` — so a single `defer db.Close()` both drains the pool and rewrites the compressed file, the same shape as a plain [`sqlite.Open`](configuration.md) or [`crypto.Open`](encryption.md).

```go
db, _ := compress.OpenSnapshot(sqlite.Config{Path: "app.db.az"}, compress.Options{})
defer db.Close()
```

It compresses the database **at rest only**: while open it runs from a full, uncompressed working copy under the OS temp directory (or `Options.TempDir`). Two consequences follow, and they are the whole reason to choose `Open` over this for a long-lived database:

- **Durability is per-session, not per-transaction.** The durable artifact is the snapshot written at `Close`; a crash while the database is open loses that session's changes (no corruption — the file reverts to its previous `Close`).
- **The working copy is plaintext on disk** for the lifetime of the handle — so this is **not** a substitute for at-rest encryption.

Opening a raw, uncompressed `.db` with `compress.OpenSnapshot` adopts it (rewritten compressed on `Close`); the on-disk file is recognised by its header, so you can point it at either form.

Opening a compressed file from an **untrusted source** can inflate a tiny crafted frame into an arbitrarily large working copy (a decompression bomb). Set `Options.MaxInflatedSize` to cap how much `OpenSnapshot` will inflate, so a malformed or hostile file fails instead.

### Shipping a compressed `.db`

To compress or inflate a `.db` without a session — for shipping, backups, or cold storage — use the file transforms:

```go
compress.Pack("app.db.az", "app.db", compress.CompressionBest) // compress an existing .db
compress.Unpack("app.db", "app.db.az")                          // inflate it back
```

## Choosing a mode

| | `Open` (live) | `OpenSnapshot` (snapshot) |
|---|---|---|
| On disk while open | compressed, in place | inflated working copy (plaintext) |
| Durability | per transaction | per session (at `Close`) |
| Survives a mid-session crash | yes | reverts to last `Close` |
| Connection pool / WAL | yes / opt-in | works, but session-scoped |
| Best for | a large database held open, crash-durable | archival, distribution, open-modify-close |

## Levels

Set the level with `Options.Level`; the zero value uses a balanced default. The ladder runs `CompressionFastest` → `CompressionFast` → `CompressionDefault` → `CompressionBetter` → `CompressionBest` (the lower levels are LZ4, the higher ones zstd). Decoding auto-detects the algorithm, so a file written at one level always reads back regardless of the level configured later. `CompressionNone` is not meaningful here (use a plain `sqlite.Open` for an uncompressed database) and falls back to the default.

## Encryption at rest

Live `Open` encrypts the database at rest when you pass `Options.Key` — each compressed block is encrypted (compress **then** encrypt, so the on-disk bytes are *both* compressed and encrypted), along with the page directory and the transient `-journal`/`-wal`. It reuses the length-preserving cipher of [`vfs/crypto`](encryption.md) (Adiantum by default, AES-XTS-256 via `Options.Cipher`); the key is the raw cipher key — 32 bytes for Adiantum, 64 for AES-XTS — and you can derive one from a passphrase with `crypto.DeriveKey`.

```go
key, _ := crypto.DeriveKey(passphrase, salt, crypto.Adiantum)
db, err := compress.Open(sqlite.Config{Path: "app.db.az"}, compress.Options{Key: key})
```

### Multiple recipients

To let several people open one encrypted database, each with their own key and no shared secret, pass `Options.Recipients` instead of `Options.Key`. A random data key encrypts the database and is wrapped once per recipient — an SSH key, a passphrase, or an age recipient built with [`crypto/keyring`](https://pkg.go.dev/gosqlite.org/crypto/keyring) — into a keyslot that any one of them can open. Reopen with `Options.Identities`; the first identity that matches a keyslot unlocks it, and none matching is `compress.ErrNoIdentity`. `Key` and `Recipients` are mutually exclusive and set only at create time.

```go
alice, _ := keyring.SSHRecipient(alicePubKey)
bob, _ := keyring.SSHRecipient(bobPubKey)
db, err := compress.Open(sqlite.Config{Path: "app.db.az"}, compress.Options{Recipients: []keyring.Recipient{alice, bob}})
```

Change who can open the database, on a closed file, with `compress.Rewrap(path, by, writeAs, membership)` (re-wrap the data key to a new membership — an access-list change) or `compress.Rekey(path, by, writeAs, membership)` (re-encrypt under a fresh data key — O(database), so a removed party is locked out cryptographically even if they kept the old key). `membership` is a `keyring.Membership{Masters, Writers, Members}`; `writeAs` is only needed for authenticated databases (see below).

### Masters (only admins change membership)

By default every recipient can `Rewrap`. To allow only designated administrators to add or remove recipients, pin one or more **masters** — ed25519 keys — with `Options.Masters` (and `Options.SignWith`, the creating master, at create). The keyslot's membership is then signed, and `Rewrap`/`Rekey` require one of the current masters (`compress.ErrNotMaster` otherwise). Readers enforce it by **pinning the masters they trust** in `Options.Masters` at open: a membership not signed by a trusted master is rejected with `compress.ErrUnauthorized` (this is the trust anchor — exactly like SSH `known_hosts`; without pinning you still read, but you get no membership-integrity guarantee). Removing a master means `Rekey` by another master, which rotates the data key so the removed master can read nothing.

```go
master, masterID, _ := keyring.GenerateMaster()        // or keyring.SSHMaster{Recipient,Identity}
db, _ := compress.Open(cfg, compress.Options{
    Masters:    []keyring.MasterRecipient{master},
    SignWith:   masterID,
    Recipients: []keyring.Recipient{alice},             // members
})
// ... later, on the closed database, only a master may change membership:
err := compress.Rewrap(path, masterID, nil, keyring.Membership{Masters: []keyring.MasterRecipient{master}, Members: []keyring.Recipient{alice, bob}})
```

### Read-only recipients (authenticated mode)

A symmetric data key means *read implies write* — anyone who can decrypt can also produce valid ciphertext. To make some recipients **read-only**, pin one or more **writers** (ed25519 keys) with `Options.Writers` (requires `Masters`). Every commit is then signed by a writer and the container carries a crypto hash per slot, so a recipient that is not a writer can read and verify but cannot produce a write others accept. A connection with a writer identity (`Options.WriteAs`) may write; without one it is **read-only** and the VFS refuses writes with `compress.ErrReadOnlyRecipient`. Readers pin `Options.Masters` (the trust anchor that authorizes the writer list); a state not signed by an authorized writer — or a tampered slot — is rejected with `compress.ErrUnauthorized`. The writer set is administered by a master via `Rewrap`/`Rekey` (which re-sign the state). This is the one mode that adds **integrity**; remove a writer or master with `Rekey`.

```go
master, masterID, _ := keyring.GenerateMaster()
db, _ := compress.Open(cfg, compress.Options{
    Masters:    []keyring.MasterRecipient{master},
    SignWith:   masterID,
    Writers:    []keyring.WriterRecipient{master}, // master is also the writer here
    WriteAs:    masterID,
    Recipients: []keyring.Recipient{alice},        // alice is read-only
})
// alice opens read-only (pinning the master) and her writes are refused:
ro, _ := compress.Open(cfg, compress.Options{Identities: []keyring.Identity{aliceID}, Masters: []keyring.MasterRecipient{master}})
```

Reopening without the key fails with `compress.ErrEncrypted`, and with the wrong key `compress.ErrWrongKey`. Outside authenticated mode the guarantee is **confidentiality at rest only** — no integrity tag, so the container checksums catch accidental corruption but not deliberate tampering, and a passive attacker still learns the container's size, geometry, and per-page compressed sizes. `OpenSnapshot` does **not** encrypt (its working copy is plaintext on disk), so use live `Open` with a `Key` for an encrypted database; for a shipped artifact, pipe [`Pack`](https://pkg.go.dev/gosqlite.org/vfs/compress) output through any encryptor.

## Module and reference

`vfs/compress` is a separate module (`gosqlite.org/vfs/compress`) so its codec dependency stays out of the core graph; `go get gosqlite.org/vfs/compress`. Full API: [pkg.go.dev/gosqlite.org/vfs/compress](https://pkg.go.dev/gosqlite.org/vfs/compress). Runnable: [`vfs/compress/example/`](../../vfs/compress/example/main.go).
