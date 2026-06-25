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

store, err := blobstore.Open(db, "files")          // creates files_objects, files_blocks, files_chunks
id, err := store.Create(ctx)                        // new empty object → int64 id

w, _ := store.Writer(ctx, id)                       // io.WriterAt + io.Closer
w.WriteAt(packet, off)                              // any offset, any order; grows on demand
w.Close()

r, _ := store.Reader(ctx, id)                       // io.ReaderAt + io.Closer
size, _ := store.Size(ctx, id)
io.Copy(dst, io.NewSectionReader(r, 0, size))       // stream out; or r.ReadAt(slice, off)
r.Close()

store.Truncate(ctx, id, n)                          // grow (sparse) or shrink (zeroes tail)
store.Delete(ctx, id)                               // frees the blocks it alone holds; ErrNotFound if gone
```

- **O(chunk) memory**, never O(object). Default chunk 64 KiB; override with `blobstore.WithChunkSize(n)` (frozen per object at Create — changing it later doesn't touch existing objects).
- **Missing id → `blobstore.ErrNotFound`** (wrapped; test with `errors.Is`).
- **Holes read as zero.** Writing at a high offset grows the object sparsely; the gap reads back as zeros.
- **Concurrency:** methods are safe for concurrent use; each op borrows one pooled conn and releases it (no per-handle pin). Writes run under `BEGIN IMMEDIATE`, so open the DB with a busy_timeout (`sqlite.OpenWAL`) so contended writes wait instead of erroring. Two writers to the *same* id are the caller's to coordinate.
- **Write throughput:** each `WriteAt` commits its own transaction, and `OpenWAL` leaves `synchronous=FULL` — an fsync per chunk. For write-heavy or streamed objects open with `synchronous=NORMAL`: commits fsync at checkpoint instead, staying crash-consistent in WAL mode (only a power loss can drop the last writes committed since the checkpoint).
- **Bulk / atomic writes:** `store.Batch(ctx, id, func(w io.WriterAt) error)` runs many writes in one transaction — atomic (rolls back entirely on error/panic) and one commit/fsync for the batch. `store.WriteAtFrom(ctx, id, off, r)` streams an `io.Reader` into an object in one `Batch`. Keep the callback tight — it holds the write lock — so buffer a slow source first.
- **Join a caller's transaction:** `store.OnConn(conn)` runs `Create`/`WriteAt`/`Batch`/`WriteAtFrom`/`Truncate`/`Delete`/`ReadAt`/`Size` on a `*sql.Conn` you already hold, so the writes join whatever transaction is open on it (a SQLite transaction is per-connection) — object content commits atomically with your own rows, one writer, no flush-before-blobstore seam. The caller owns `BEGIN`/`COMMIT`/`ROLLBACK`; a read through the handle sees the transaction's own uncommitted writes; the post-delete/truncate `incremental_vacuum` is yours to run after commit.
- **Reclaim space:** `blobstore.WithVacuumOnDelete()` issues `PRAGMA incremental_vacuum` after frees — effective only if the DB is in incremental auto_vacuum mode (open with `Config.Pragmas.AutoVacuum = sqlite.AutoVacuumIncremental`, or convert an existing DB with `db.SetAutoVacuum`).
- **Compression:** `blobstore.Open(db, name, blobstore.WithCompression(blobstore.CompressionDefault))` stores objects compressed (levels `CompressionFastest`…`CompressionBest`; default `CompressionNone`). Same `Writer`/`Reader` API. Choose the mode per object with `WithObjectCompression` at `Create`, convert an existing object raw↔compressed or change its level later with `SetCompression` (a mode change rewrites the chunks), and read at-rest size/ratio with `Stat`; raw and compressed objects coexist; incompressible chunks fall back to verbatim. Cost: a compressed object works a whole chunk in memory (partial writes read-modify-write), so it fits write-once / sequential / compressible data, not hot random partial updates. Prefer a larger `WithChunkSize` when compressing. The same codec is exposed standalone as `blobstore.Compress(plain, level)` / `blobstore.Decompress(data, enc, maxSize)` for a tiny value you keep outside the store (inlined in its own row) under the identical on-disk encoding.
- **Clone & versions (copy-on-write):** chunk bytes live in reference-counted blocks, so `store.Clone(ctx, srcID)` makes an identical object in O(metadata) — no bytes copied — and the two diverge copy-on-write. `store.NewVersion(ctx, id)` snapshots an object the same way; `ListVersions`/`OpenVersion` read versions back immutably; a per-object retention `Policy` (`WithObjectVersioning` at Create, or `SetRetention`) plus `Prune` bound how many/old versions are kept. `Stat` splits stored bytes into `UniqueBytes` and `SharedBytes`.
- **Dedup:** `blobstore.WithDedup()` stores byte-identical full blocks once across objects (a content hash per full-block write; not applied to raw in-place partial writes).
- **Read-only:** `blobstore.OpenReadOnly(db, name)` reattaches to a provisioned store with no DDL (snapshot browsing / read-only media); every mutator returns `blobstore.ErrReadOnly`.

### The one footgun

**Do NOT back a Store with a private `OpenInMemory()` database.** Every operation borrows a connection from the pool, so each pooled conn would see its own empty in-memory DB and writes would appear to vanish. Use a **file** (`OpenWAL`), `sqlite.OpenShared(name)`, or a pool with `SetMaxOpenConns(1)`.

## When you'd hand-roll instead — and why not to

For a fixed, pre-sized blob, `Conn.OpenBlob` (or `ext/blobio`) is right. But to *grow* a value, never do `UPDATE col = col || zeroblob(delta)`: SQLite silently drops `zeroblob` operands under `||`, producing a shorter blob and no error. Pre-size with `zeroblob(N)` at INSERT/UPDATE and write into it, or use blobstore, which removes the chunking entirely.

Runnable: [`blobstore/example`](../../blobstore/example/main.go). Full API reference: [pkg.go.dev/gosqlite.org/blobstore](https://pkg.go.dev/gosqlite.org/blobstore).
