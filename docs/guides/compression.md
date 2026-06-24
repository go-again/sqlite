---
title: Compressed databases (vfs/vault)
description: Store a SQLite database in a vfs/vault container — vault.Open queries it compressed in place (durable per transaction, multi-connection, optional WAL); vault.OpenSnapshot inflates a working copy; plus Pack / Unpack for shipping, and Compact / Trim to reclaim space. Compression and encryption are independent options.
sidebar:
  order: 19
---

# Compressed databases (`vfs/vault`)

This is the [`vfs/vault`](vault.md) container in its compression role: it stores a SQLite database in a block-structured **container** where **compression and encryption are independent options** — plain, compressed, encrypted, or both. This page covers the storage modes and the compression level ladder; for the full container story — encryption, multiple recipients, masters and signing writers (with read-only members), tamper-evidence, and rollback resistance — see the [vault container guide](vault.md), the same container with `Options.Key`/`Recipients` set.

Two modes, picked by how the database is used:

- **[Live](#live-in-place--open) — `vault.Open`:** query the database while it stays in container form on disk, durable per transaction, with a connection pool and optional WAL. Reach for this for a large database that stays open and must survive a crash mid-session.
- **[Snapshot](#snapshot-inflate-for-the-session--opensnapshot) — `vault.OpenSnapshot`:** inflate the whole database into a private working copy for the session, repack it at `Close`. Reach for this for archival, distribution, backups, and open-modify-close tooling (no encryption — the working copy is plaintext).

Both are pure Go and share the same level ladder.

## Live: in place — `Open`

`vault.Open` hands back a normal database handle whose on-disk file stays in container form the entire time it is open. It is a real storage engine — a pure-Go, file-backed VFS that translates SQLite's page reads and writes to block-aligned slots in a block-structured container — so **nothing is ever written to disk in a weaker form than configured** (never uncompressed when compression is on, never plaintext when a key is set).

```go
import "gosqlite.org/vfs/vault"

db, _ := vault.Open(sqlite.Config{Path: "app.db"}, vault.Options{Level: vault.CompressionDefault})
defer db.Close()
// use db exactly like *sql.DB — query, exec, transactions, a connection pool
```

- **Durable per transaction.** Each commit atomically flips a ping-pong superblock, so a crash leaves the previous committed state intact and SQLite's rollback journal recovers the rest — this is fault-injection tested at every step of the commit.
- **Multiple connections.** A connection pool is safe: connections that open the same path share one in-memory container and coordinate through the VFS's in-process advisory locks — many readers, one writer at a time.
- **Rollback journal by default; WAL optional.** Set `Pragmas.JournalMode` to WAL to opt in. In WAL mode the main database stays in container form and only the transient `-wal` frames are written plainly (folded back into container slots on checkpoint). WAL coordination is in-process — multiple connections within one process.
- **Compressed at rest.** With `Options.Level` set, on log/JSON-shaped data at the default large page size the on-disk container is a small fraction of the logical database. The default (`Options{}`) is a plain container — set a level to compress.

`vault.Open` refuses to open a file that is not one of its containers rather than risk clobbering it, and rejects a malformed or hostile container with an error instead of trusting it.

It is a good fit for a large database that must stay open continuously and survive crashes — and a poor fit for hot, random small writes (every page write re-encodes).

## Snapshot: inflate for the session — `OpenSnapshot`

`vault.OpenSnapshot` inflates the file into a private working copy, opens that copy as an ordinary database, and repacks it back over the original path at `Close` — so a single `defer db.Close()` both drains the pool and rewrites the file, the same shape as a plain [`sqlite.Open`](configuration.md).

```go
db, _ := vault.OpenSnapshot(sqlite.Config{Path: "app.db"}, vault.Options{Level: vault.CompressionDefault})
defer db.Close()
```

It packs the database **at rest only**: while open it runs from a full working copy under the OS temp directory (or `Options.TempDir`). Two consequences follow, and they are the whole reason to choose `Open` over this for a long-lived database:

- **Durability is per-session, not per-transaction.** The durable artifact is the snapshot written at `Close`; a crash while the database is open loses that session's changes (no corruption — the file reverts to its previous `Close`).
- **The working copy is plaintext on disk** for the lifetime of the handle — so `OpenSnapshot` is **not** a substitute for at-rest encryption; use live `Open` with a `Key` for an encrypted database.

Opening a raw, uncompressed `.db` with `vault.OpenSnapshot` adopts it (rewritten on `Close`); the on-disk file is recognised by its header, so you can point it at either form. Opening a file from an **untrusted source** can inflate a tiny crafted frame into an arbitrarily large working copy (a decompression bomb) — set `Options.MaxInflatedSize` to cap how much `OpenSnapshot` will inflate.

### Shipping a `.db`

To compress or inflate a `.db` without a session — for shipping, backups, or cold storage — use the file transforms:

```go
vault.Pack("app.db.az", "app.db", vault.CompressionBest) // pack an existing .db
vault.Unpack("app.db", "app.db.az")                       // inflate it back
```

## Choosing a mode

| | `Open` (live) | `OpenSnapshot` (snapshot) |
|---|---|---|
| On disk while open | container, in place | inflated working copy (plaintext) |
| Durability | per transaction | per session (at `Close`) |
| Survives a mid-session crash | yes | reverts to last `Close` |
| Connection pool / WAL | yes / opt-in | works, but session-scoped |
| Encryption at rest | yes (`Options.Key`/`Recipients`) | no (plaintext working copy) |
| Best for | a large database held open, crash-durable | archival, distribution, open-modify-close |

## Levels

Set the level with `Options.Level`. The zero value, `CompressionNone`, is **off** — pages are stored raw (a plain container). Set a level to compress: the ladder runs `CompressionFastest` → `CompressionFast` → `CompressionDefault` → `CompressionBetter` → `CompressionBest` (the lower levels are LZ4, the higher ones zstd). Decoding auto-detects the algorithm, so a file written at one level (or raw) always reads back regardless of the level configured later.

## Reclaiming space — `Compact` and `Trim`

Under churn the container reuses freed blocks, so the at-rest file plateaus rather than growing — but it does not shrink mid-session by design. `vault.Compact(cfg, opts)` is the offline, densest reclaim: on a **closed** database it rewrites the live pages into a fresh, densely-packed file and atomically replaces the original, returning the freed blocks to the filesystem. It preserves the encryption and authenticated mode (pass the same `Options`) and continues the commit generation, so an [`Options.Anchor`](vault.md) stays valid across compaction.

```go
err := vault.Compact(sqlite.Config{Path: "app.db"}, vault.Options{Level: vault.CompressionDefault, Key: key})
```

`vault.Trim(path, maxBytes)` is the online counterpart: it returns **trailing** free blocks to the OS while the database stays open (a cheap truncate, no page relocation), so a mounted container can shrink mid-session when free blocks have collected at the tail. `Compact` remains the densest, layout-independent reclaim. To hand someone an encrypted, compressed point-in-time backup at a new path, use `vault.Snapshot` (see the [vault guide](vault.md)).

## Encryption

The same container encrypts at rest when you set `Options.Key` (a raw key) or `Options.Recipients` (a wrapped data key, several parties each with their own key) — compress **then** encrypt, so the on-disk bytes are both. Add `Options.Authenticate` (or `Options.Writers`) for tamper-evidence, and `Options.Anchor` for rollback resistance. The full crypto story — recipients, masters, signing writers (with read-only members), authenticated mode, the external replay anchor, and membership enumeration — is in the [vault container guide](vault.md).

## Module and reference

`vfs/vault` is a separate module (`gosqlite.org/vfs/vault`) so its codec and crypto dependencies stay out of the core graph; `go get gosqlite.org/vfs/vault`. Full API: [pkg.go.dev/gosqlite.org/vfs/vault](https://pkg.go.dev/gosqlite.org/vfs/vault). Runnable: [`vfs/vault/example/`](../../vfs/vault/example/main.go) (the whole option matrix) and [`examples/vault-blobstore`](../../examples/vault-blobstore/main.go) (a blobstore over a multi-recipient, tamper-evident container).
