---
title: Blob storage
description: Store large, growable, randomly-writable byte objects (files, uploads, streamed content) in SQLite as io.ReaderAt / io.WriterAt, with O(chunk) memory.
sidebar:
  order: 18
---

# Blob storage

`blobstore` is the supported way to keep a large, growable byte object in SQLite — a file, an upload, streamed content — addressed by offset and read or written in slices, never materialized whole. It manages the chunking and storage for you and hands back an [`io.ReaderAt`](https://pkg.go.dev/io#ReaderAt) / [`io.WriterAt`](https://pkg.go.dev/io#WriterAt) per object.

## Why not just a BLOB column?

SQLite's incremental BLOB I/O (`(*sqlite.Conn).OpenBlob`) is **fixed-size**: a value can't grow once allocated. The tempting "grow it" trick is a silent-corruption trap — SQLite drops `zeroblob` operands under `||`, so `UPDATE col = col || zeroblob(delta)` produces a *shorter* value with no error. For an unknown or growing size the only correct hand-rolled answer is a chunk table plus read clamping and sparse zero-fill. `blobstore` is exactly that, written and tested once.

## Shape

```go
import "gosqlite.org/blobstore"

store, _ := blobstore.Open(db, "files")     // creates files_objects, files_blocks, files_chunks
id, _ := store.Create(ctx)                    // a new empty object → int64 id

w, _ := store.Writer(ctx, id)                 // io.WriterAt + io.Closer
w.WriteAt(packet, off)                         // any offset, any order; grows on demand
w.Close()

r, _ := store.Reader(ctx, id)                 // io.ReaderAt + io.Closer
size, _ := store.Size(ctx, id)
io.Copy(dst, io.NewSectionReader(r, 0, size)) // stream out; or r.ReadAt(slice, off)
r.Close()

store.Truncate(ctx, id, n)                    // grow (sparse) or shrink (zeroes the tail)
store.Delete(ctx, id)                          // frees the blocks it alone holds; blobstore.ErrNotFound if gone
```

- **O(chunk) memory**, never O(object). Default chunk 64 KiB; set per-Store with `blobstore.WithChunkSize(n)` (frozen per object at `Create`, so changing the default never disturbs existing objects).
- **Sparse holes read as zero** — writing at a high offset grows the object sparsely.
- **No growable-value trap** — chunks are fixed-size and never extended in place (a raw chunk is a `zeroblob` written in place, a compressed chunk a whole value), so the `||`/`zeroblob` truncation trap never enters the picture.
- **Missing id** → `blobstore.ErrNotFound` (wrapped; use `errors.Is`).

## Concurrency and the backing database

Every operation borrows a connection from the pool, runs its SQL and BLOB I/O on that one physical connection, and releases it — so any number of objects can be open at once without pinning a connection per handle. Writes run under `BEGIN IMMEDIATE`, so SQLite serializes concurrent writers; open the database with a busy timeout (`sqlite.OpenWAL`) so a contended write waits instead of failing with `SQLITE_BUSY`.

Because each operation uses a *pooled* connection, the database must be one every connection shares: a **file**, [`sqlite.OpenShared`](configuration.md), or a pool with `SetMaxOpenConns(1)`. A private `sqlite.OpenInMemory()` gives each connection its own empty database, so writes would appear to vanish — use `OpenShared` for in-memory use.

To return freed pages to the OS on delete or shrink, open the database in incremental auto-vacuum mode and pass `blobstore.WithVacuumOnDelete()`.

For write-heavy or streamed objects, consider opening the database with `synchronous = NORMAL`. `OpenWAL` leaves SQLite's default (`FULL`), which fsyncs the WAL on every commit — and because each `WriteAt` is its own transaction, that is one fsync per chunk. `NORMAL` defers the fsync to checkpoint, so commits are cheap; in WAL mode it stays crash-consistent (no corruption) across application and OS crashes, and only a hard power loss can drop transactions committed since the last checkpoint — an appropriate trade for blob storage.

## Bulk and atomic writes

`WriteAt` commits each write in its own transaction. To run many writes in one transaction — amortizing the per-write commit (and fsync) for a bulk load or stream, and committing them atomically — use `store.Batch`:

```go
err := store.Batch(ctx, id, func(w io.WriterAt) error {
    if _, err := w.WriteAt(head, 0); err != nil {
        return err
    }
    _, err := w.WriteAt(tail, int64(len(head)))
    return err
})
```

Every write in the callback commits together when it returns `nil`, or rolls back entirely if it returns an error (or panics) — so a half-written batch never persists. The `io.WriterAt` is bound to one transaction (drive it sequentially), and `Batch` holds the write lock for the whole callback, so keep it tight: buffer a slow source first rather than reading it inside. `store.WriteAtFrom(ctx, id, off, r)` is the convenience form — it streams an `io.Reader` into an object in one `Batch`.

## Compression

Open a Store with `blobstore.WithCompression(level)` to store its objects compressed — the same `Writer`/`Reader` API, transparently:

```go
store, _ := blobstore.Open(db, "files", blobstore.WithCompression(blobstore.CompressionDefault))
```

Levels run `CompressionFastest` → `CompressionFast` → `CompressionDefault` → `CompressionBetter` → `CompressionBest` (the codec is abstracted; the zero value `CompressionNone` stores raw). Each chunk is stored as a whole compressed value; an incompressible chunk falls back to verbatim, so a chunk is never stored larger than its payload. Each object's mode is chosen at `Create` — a Store reads any object regardless of its mode, so raw and compressed objects coexist. Sparse holes stay free (an unwritten chunk stores nothing).

Override compression for one object with `blobstore.WithObjectCompression(level)` at `Create` — `CompressionNone` stores it raw, any level stores it compressed *at that level*, regardless of the Store default (e.g. `store.Create(ctx, blobstore.WithObjectCompression(blobstore.CompressionBest))`). Objects of different modes and levels coexist in one store, so you can compress small or cold objects hard while keeping large or hot ones raw or lightly compressed for speed.

`store.SetCompression(ctx, id, level)` changes an existing object. Changing only the *level* of a compressed object rewrites nothing — reads are level-agnostic, so an object can hold chunks at different levels (write a head at `CompressionBest`, lower the level, append a large tail). Changing the *mode* — raw↔compressed, including `CompressionNone` to go raw — converts every existing chunk in one transaction (an O(object size) pass), so you can compress an object you first stored raw, or decompress one for fast in-place random I/O. `store.Stat(ctx, id)` returns an object's metadata — logical size, stored bytes (split into `UniqueBytes` held by this object alone and `SharedBytes` held in common with a clone or version), the actual at-rest compression **ratio** (computed from the block sizes, not a maintained column), chunk size, mode, and current level.

The trade-off: a compressed object can't use in-place incremental BLOB I/O, so every operation works on a full chunk in memory — a read decompresses the whole chunk, and a partial write read-modify-writes it (a write covering a whole chunk skips the read). Compression fits write-once / read-mostly or sequentially-streamed compressible data (files, logs, JSON), not hot random partial updates or already-compressed payloads. Prefer a larger `WithChunkSize` when compressing. It composes with [encryption](encryption.md): chunks are compressed before the VFS encrypts the pages — the correct order.

## Clones, snapshots, and versions

Chunk bytes live in reference-counted blocks, so objects can share content with no copy. `store.Clone(ctx, srcID)` makes a new object identical to an existing one in O(metadata) — it duplicates the chunk mapping and bumps refcounts, never the bytes — and the two diverge copy-on-write as either is written, allocating new blocks only for the chunks that change. `store.Stat` reports the split as `UniqueBytes` (blocks this object alone holds, reclaimed if it is deleted) and `SharedBytes` (blocks held in common with a clone or version).

A **version** is a copy-on-write snapshot of an object's content at a point in time, built on the same machinery: `store.NewVersion(ctx, id)` records one (O(metadata), sharing every block with the live object until it diverges) and returns its version number; `store.ListVersions(ctx, id)` enumerates them; `store.OpenVersion(ctx, id, versionNo)` returns an immutable `io.ReaderAt` over that snapshot. A per-object retention **policy** bounds how many or how old versions are kept — set it at `Create` with `blobstore.WithObjectVersioning(blobstore.Policy{KeepVersions: 10, MaxAge: 30 * 24 * time.Hour})` or later with `store.SetRetention`; `store.Prune` (and the sweep after each `NewVersion`) enforces it, freeing the blocks a dropped version alone held. Deleting an object removes its versions and their snapshots too.

## Deduplication

`blobstore.WithDedup()` turns on content-addressed deduplication: a full-block write whose bytes are byte-identical to an already-stored block references that block (bumping its refcount) instead of writing a copy, so identical content is stored once across objects. It costs a content hash per full-block write — leave it off for write-hot or already-unique data. It applies to compressed objects and full-chunk writes; raw in-place partial writes mutate their block directly and are not deduplicated.

## Read-only and snapshot browsing

`blobstore.OpenReadOnly(db, name)` reattaches to an already-provisioned store without issuing any DDL, so it works against a read-only database — browsing a snapshot, or mounting an image on read-only media. It errors if the store is not already provisioned (open it writable once with `Open` first), and every mutating method returns `blobstore.ErrReadOnly`; reads behave exactly as on a writable handle.

## Encryption at rest

A blobstore is just SQL and incremental BLOB I/O over a database, so it inherits whatever the underlying VFS provides — including encryption, with no blobstore-specific configuration. Open the store's database through [`vfs/crypto`](encryption.md) and hand the returned `*sqlite.DB` to `blobstore.Open`: every object, chunk, and block is encrypted on disk (runnable: [`examples/encrypted-blobstore`](../../examples/encrypted-blobstore/main.go)). For multi-recipient or tamper-evident encryption under a store, the same composition works with [`vfs/vault`](encryption.md) (runnable: [`examples/vault-blobstore`](../../examples/vault-blobstore/main.go)).

```go
db, _ := crypto.Open(sqlite.Config{Path: "store.db"}, crypto.Options{Key: key})
store, _ := blobstore.Open(db, "files") // every object the store writes is encrypted on disk
```

Runnable: [`blobstore/example/`](../../blobstore/example/main.go), and [`examples/encrypted-blobstore`](../../examples/encrypted-blobstore/main.go) (encrypted at rest, confirm no plaintext on disk).
