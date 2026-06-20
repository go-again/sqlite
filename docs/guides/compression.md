---
title: Compressed databases
description: Store a SQLite database compressed on disk with gosqlite — compress.Open inflates it for a session and recompresses on close, plus Pack / Unpack for shipping a compressed .db.
sidebar:
  order: 19
---

# Compressed databases

`compress.Open` keeps a SQLite database compressed on disk and hands back a normal database handle. It inflates the compressed file into a private working copy, opens that copy, and recompresses it back over the original path when you close the handle — so a single `defer db.Close()` both drains the pool and rewrites the compressed file, the same shape as a plain [`sqlite.Open`](configuration.md) or [`crypto.Open`](encryption.md).

```go
import "gosqlite.org/vfs/compress"

db, _ := compress.Open(sqlite.Config{Path: "app.db.az"}, compress.Options{})
defer db.Close()
// use db exactly like *sql.DB — query, exec, transactions
```

To compress or inflate a `.db` without a session — for shipping, backups, or cold storage — use the file transforms:

```go
compress.Pack("app.db.az", "app.db", compress.CompressionBest) // compress an existing .db
compress.Unpack("app.db", "app.db.az")                          // inflate it back
```

## Snapshot model — read this first

This compresses a database **at rest**. While it is open, it runs from a full, uncompressed working copy under the OS temp directory (or `Options.TempDir`); the compressed file is rewritten only at `Close`. Two consequences follow, and they are the whole reason to reach for this instead of a plain database:

- **Durability is per-session, not per-transaction.** The durable artifact is the snapshot written at `Close`. A crash *while the database is open* leaves the on-disk file at its previous `Close` — no corruption, but changes made in the interrupted session are lost. A plain database (or an encrypting VFS) is durable per committed transaction; this is not.
- **The working copy is plaintext on disk** for the lifetime of the handle. So this is **not** a substitute for at-rest encryption.

That makes it a good fit for archival, distribution, backups, and open-modify-close tooling over compressible data — and a poor fit for a large database that must stay open continuously, or that must survive a crash mid-session.

## Levels

Set the level with `Options.Level`; the zero value uses a balanced default. The ladder runs `CompressionFastest` → `CompressionFast` → `CompressionDefault` → `CompressionBetter` → `CompressionBest` (the lower levels are LZ4, the higher ones zstd). Decoding auto-detects the algorithm, so a file written at one level always reads back regardless of the level configured later. `CompressionNone` is not meaningful here (use a plain `sqlite.Open` for an uncompressed database) and falls back to the default.

## Adopting an existing database

Opening a raw, uncompressed `.db` with `compress.Open` adopts it: the file is rewritten compressed on `Close`. The on-disk file is recognised by its header, so you can point `compress.Open` at either form.

## Combining with encryption

At-rest encryption of the compressed file is not built in here. Because the working copy is plaintext, encrypting it during the session would have to happen underneath this package, and compressing already-encrypted data saves nothing — so transparent, per-transaction compression *and* encryption together is a job for a live compressing VFS composed with [`vfs/crypto`](encryption.md), not this snapshot mode. For a shipped artifact, pipe [`Pack`](https://pkg.go.dev/gosqlite.org/vfs/compress) output through any encryptor.

## Module and reference

`vfs/compress` is a separate module (`gosqlite.org/vfs/compress`) so its codec dependency stays out of the core graph; `go get gosqlite.org/vfs/compress`. Full API: [pkg.go.dev/gosqlite.org/vfs/compress](https://pkg.go.dev/gosqlite.org/vfs/compress). Runnable: [`vfs/compress/example/`](../../vfs/compress/example/main.go).
