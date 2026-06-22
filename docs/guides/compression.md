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

Reopening without the key fails with `compress.ErrEncrypted`, and with the wrong key `compress.ErrWrongKey`. Like `vfs/crypto`, the guarantee is **confidentiality at rest only** — no integrity tag, so the container checksums catch accidental corruption but not deliberate tampering, and a passive attacker still learns the container's size, geometry, and per-page compressed sizes. `OpenSnapshot` does **not** encrypt (its working copy is plaintext on disk), so use live `Open` with a `Key` for an encrypted database; for a shipped artifact, pipe [`Pack`](https://pkg.go.dev/gosqlite.org/vfs/compress) output through any encryptor.

## Module and reference

`vfs/compress` is a separate module (`gosqlite.org/vfs/compress`) so its codec dependency stays out of the core graph; `go get gosqlite.org/vfs/compress`. Full API: [pkg.go.dev/gosqlite.org/vfs/compress](https://pkg.go.dev/gosqlite.org/vfs/compress). Runnable: [`vfs/compress/example/`](../../vfs/compress/example/main.go).
