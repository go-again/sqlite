---
name: blob-storage
description: Use when storing large, growable, or randomly-writable byte objects (files, uploads, streamed content) in SQLite with gosqlite — the blobstore package, plus when to reach for the fixed-size Conn.OpenBlob / ext/blobio instead.
---

# Storing large byte objects in SQLite

Three layers, pick by need:

| Need | Use |
|---|---|
| A **growable / unknown-size** stream, random `WriteAt`/`ReadAt`, sparse holes | **`gosqlite.org/blobstore`** |
| Incremental I/O on a **fixed-size**, already-sized BLOB column from Go | `(*sqlite.Conn).OpenBlob` |
| Incremental I/O on a fixed-size BLOB **from SQL** | `ext/blobio` (`readblob`/`writeblob`) |

## blobstore — the growable object store

```go
import "gosqlite.org/blobstore"

store, err := blobstore.Open(db, "files")          // creates files_objects + files_chunks
id, err := store.Create(ctx)                        // new empty object → int64 id

w, _ := store.Writer(ctx, id)                       // io.WriterAt + io.Closer
w.WriteAt(packet, off)                              // any offset, any order; grows on demand
w.Close()

r, _ := store.Reader(ctx, id)                       // io.ReaderAt + io.Closer
size, _ := store.Size(ctx, id)
io.Copy(dst, io.NewSectionReader(r, 0, size))       // stream out; or r.ReadAt(slice, off)
r.Close()

store.Truncate(ctx, id, n)                          // grow (sparse) or shrink (zeroes tail)
store.Delete(ctx, id)                               // frees every chunk; ErrNotFound if gone
```

- **O(chunk) memory**, never O(object). Default chunk 64 KiB; override with `blobstore.WithChunkSize(n)` (frozen per object at Create — changing it later doesn't touch existing objects).
- **Missing id → `blobstore.ErrNotFound`** (wrapped; test with `errors.Is`).
- **Holes read as zero.** Writing at a high offset grows the object sparsely; the gap reads back as zeros.
- **Concurrency:** methods are safe for concurrent use; each op borrows one pooled conn and releases it (no per-handle pin). Writes run under `BEGIN IMMEDIATE`, so open the DB with a busy_timeout (`sqlite.OpenWAL`) so contended writes wait instead of erroring. Two writers to the *same* id are the caller's to coordinate.
- **Reclaim space:** `blobstore.WithVacuumOnDelete()` issues `PRAGMA incremental_vacuum` after frees — effective only if the DB is in incremental auto_vacuum mode (open with `Config.Pragmas.AutoVacuum = sqlite.AutoVacuumIncremental`, or convert an existing DB with `db.SetAutoVacuum`).
- **Compression:** `blobstore.Open(db, name, blobstore.WithCompression(blobstore.CompressionDefault))` stores objects compressed (levels `CompressionFastest`…`CompressionBest`; default `CompressionNone`). Same `Writer`/`Reader` API. Frozen per object at `Create`; raw and compressed objects coexist; incompressible chunks fall back to verbatim. Cost: a compressed object works a whole chunk in memory (partial writes read-modify-write), so it fits write-once / sequential / compressible data, not hot random partial updates. Prefer a larger `WithChunkSize` when compressing.

### The one footgun

**Do NOT back a Store with a private `OpenInMemory()` database.** Every operation borrows a connection from the pool, so each pooled conn would see its own empty in-memory DB and writes would appear to vanish. Use a **file** (`OpenWAL`), `sqlite.OpenShared(name)`, or a pool with `SetMaxOpenConns(1)`.

## When you'd hand-roll instead — and why not to

For a fixed, pre-sized blob, `Conn.OpenBlob` (or `ext/blobio`) is right. But to *grow* a value, never do `UPDATE col = col || zeroblob(delta)`: SQLite silently drops `zeroblob` operands under `||`, producing a shorter blob and no error. Pre-size with `zeroblob(N)` at INSERT/UPDATE and write into it, or use blobstore, which removes the chunking entirely.

Runnable: [`blobstore/example`](../../blobstore/example/main.go). Fixed-size reference doc comments: [pkg.go.dev/gosqlite.org/blobstore](https://pkg.go.dev/gosqlite.org/blobstore).
