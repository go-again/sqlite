---
title: Blob storage
description: Store large, growable, randomly-writable byte objects (files, uploads, streamed content) in SQLite as io.ReaderAt / io.WriterAt, with O(chunk) memory.
sidebar:
  order: 18
---

# Blob storage

`blobstore` is the supported way to keep a large, growable byte object in SQLite — a file, an upload, streamed content — addressed by offset and read or written in slices, never materialized whole. It manages a chunk table for you and hands back an [`io.ReaderAt`](https://pkg.go.dev/io#ReaderAt) / [`io.WriterAt`](https://pkg.go.dev/io#WriterAt) per object.

## Why not just a BLOB column?

SQLite's incremental BLOB I/O (`(*sqlite.Conn).OpenBlob`) is **fixed-size**: a value can't grow once allocated. The tempting "grow it" trick is a silent-corruption trap — SQLite drops `zeroblob` operands under `||`, so `UPDATE col = col || zeroblob(delta)` produces a *shorter* value with no error. For an unknown or growing size the only correct hand-rolled answer is a chunk table plus read clamping and sparse zero-fill. `blobstore` is exactly that, written and tested once.

## Shape

```go
import "gosqlite.org/blobstore"

store, _ := blobstore.Open(db, "files")     // creates files_objects + files_chunks
id, _ := store.Create(ctx)                    // a new empty object → int64 id

w, _ := store.Writer(ctx, id)                 // io.WriterAt + io.Closer
w.WriteAt(packet, off)                         // any offset, any order; grows on demand
w.Close()

r, _ := store.Reader(ctx, id)                 // io.ReaderAt + io.Closer
size, _ := store.Size(ctx, id)
io.Copy(dst, io.NewSectionReader(r, 0, size)) // stream out; or r.ReadAt(slice, off)
r.Close()

store.Truncate(ctx, id, n)                    // grow (sparse) or shrink (zeroes the tail)
store.Delete(ctx, id)                          // frees every chunk; blobstore.ErrNotFound if gone
```

- **O(chunk) memory**, never O(object). Default chunk 64 KiB; set per-Store with `blobstore.WithChunkSize(n)` (frozen per object at `Create`, so changing the default never disturbs existing objects).
- **Sparse holes read as zero** — writing at a high offset grows the object sparsely.
- **No growable-value trap** — each chunk is allocated full once with `zeroblob` and written in place; values never grow, so `||` never enters the picture.
- **Missing id** → `blobstore.ErrNotFound` (wrapped; use `errors.Is`).

## Concurrency and the backing database

Every operation borrows a connection from the pool, runs its SQL and BLOB I/O on that one physical connection, and releases it — so any number of objects can be open at once without pinning a connection per handle. Writes run under `BEGIN IMMEDIATE`, so SQLite serializes concurrent writers; open the database with a busy timeout (`sqlite.OpenWAL`) so a contended write waits instead of failing with `SQLITE_BUSY`.

Because each operation uses a *pooled* connection, the database must be one every connection shares: a **file**, [`sqlite.OpenShared`](configuration.md), or a pool with `SetMaxOpenConns(1)`. A private `sqlite.OpenInMemory()` gives each connection its own empty database, so writes would appear to vanish — use `OpenShared` for in-memory use.

To return freed pages to the OS on delete or shrink, open the database in incremental auto-vacuum mode and pass `blobstore.WithVacuumOnDelete()`.

## Compression

Open a Store with `blobstore.WithCompression(level)` to store its objects compressed — the same `Writer`/`Reader` API, transparently:

```go
store, _ := blobstore.Open(db, "files", blobstore.WithCompression(blobstore.CompressionDefault))
```

Levels run `CompressionFastest` → `CompressionFast` → `CompressionDefault` → `CompressionBetter` → `CompressionBest` (the codec is abstracted; the zero value `CompressionNone` stores raw). Each chunk is stored as a whole compressed value; an incompressible chunk falls back to verbatim, so a chunk is never stored larger than its payload. Each object's mode is chosen at `Create` — a Store reads any object regardless of its mode, so raw and compressed objects coexist. Sparse holes stay free (an unwritten chunk stores nothing).

Override compression for one object with `blobstore.WithObjectCompression(level)` at `Create` — `CompressionNone` stores it raw, any level stores it compressed *at that level*, regardless of the Store default (e.g. `store.Create(ctx, blobstore.WithObjectCompression(blobstore.CompressionBest))`). Objects of different modes and levels coexist in one store, so you can compress small or cold objects hard while keeping large or hot ones raw or lightly compressed for speed.

`store.SetCompression(ctx, id, level)` changes an existing object. Changing only the *level* of a compressed object rewrites nothing — reads are level-agnostic, so an object can hold chunks at different levels (write a head at `CompressionBest`, lower the level, append a large tail). Changing the *mode* — raw↔compressed, including `CompressionNone` to go raw — converts every existing chunk in one transaction (an O(object size) pass), so you can compress an object you first stored raw, or decompress one for fast in-place random I/O. `store.Stat(ctx, id)` returns an object's metadata — logical size, stored bytes, the actual at-rest compression **ratio** (computed from the chunk sizes, not a maintained column), chunk size, mode, and current level.

The trade-off: a compressed object can't use in-place incremental BLOB I/O, so every operation works on a full chunk in memory — a read decompresses the whole chunk, and a partial write read-modify-writes it (a write covering a whole chunk skips the read). Compression fits write-once / read-mostly or sequentially-streamed compressible data (files, logs, JSON), not hot random partial updates or already-compressed payloads. Prefer a larger `WithChunkSize` when compressing. It composes with [encryption](encryption.md): chunks are compressed before the VFS encrypts the pages — the correct order.

Runnable: [`blobstore/example/`](../../blobstore/example/main.go).
