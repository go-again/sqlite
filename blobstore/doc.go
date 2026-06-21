// Package blobstore stores large, growable, randomly-writable byte objects
// in SQLite with O(chunk) memory — never materializing a whole object.
//
// SQLite's incremental BLOB I/O ([gosqlite.org.Conn.OpenBlob]) is fixed-size:
// a value cannot grow once allocated, and the usual "grow with
// `col || zeroblob(delta)`" trick silently truncates (SQLite drops zeroblob
// operands under `||`). blobstore is the supported answer for an
// unknown-or-growing size: it manages a chunk table for you and exposes each
// object as an [io.ReaderAt] / [io.WriterAt], filling the gap that every
// "put a file in SQLite" app otherwise re-derives by hand.
//
// # Model
//
// Each object is a sequence of fixed-size chunks. A chunk is allocated full
// (via zeroblob, the one correct use) the first time any byte in it is
// written, then written in place with incremental BLOB I/O — so a value never
// grows and the zeroblob/`||` trap never arises. Growth is just new chunk
// rows. The object's logical size is authoritative: reads clamp to it, and a
// chunk that was never written reads back as zeros (sparse holes are free).
//
// # Usage
//
//	store, err := blobstore.Open(db, "files")           // creates files_objects + files_chunks
//	id, err := store.Create(ctx)                         // a new empty object
//
//	w, err := store.Writer(ctx, id)                      // io.WriterAt + io.Closer
//	w.WriteAt(packet, off)                               // out-of-order offsets are fine
//	w.Close()
//
//	r, err := store.Reader(ctx, id)                      // io.ReaderAt + io.Closer
//	io.Copy(dst, io.NewSectionReader(r, 0, must(store.Size(ctx, id))))
//	r.Close()
//
//	store.Truncate(ctx, id, 100)                         // grow (sparse) or shrink
//	store.Delete(ctx, id)                                // frees every chunk
//
// Open creates two tables from the name you pass — "<name>_objects" (one row
// per object: id, logical size, chunk size) and "<name>_chunks" (the data,
// one rowid-addressable BLOB per chunk). The name is validated as a SQL
// identifier. Both tables share whatever database the [gosqlite.org.DB] points
// at; put other application tables in the same database freely.
//
// # Concurrency
//
// Methods are safe for concurrent use. Each operation borrows one pooled
// connection for its duration and releases it — so any number of objects can
// be open at once without pinning a connection per handle. Writes run under
// BEGIN IMMEDIATE, so SQLite serializes concurrent writers; use a database
// opened with a busy_timeout (e.g. [gosqlite.org.OpenWAL]) so a contended
// write waits rather than failing with SQLITE_BUSY. Two writers to the SAME
// object id are last-writer-wins per byte range and are the caller's to
// coordinate; distinct objects never conflict logically.
//
// A [Writer] / [Reader] carries the context passed to [Store.Writer] /
// [Store.Reader]; that context governs every WriteAt / ReadAt on the handle.
//
// Because every operation borrows a connection from the pool, the database
// must be one that all pooled connections share: a file-backed database, or
// [gosqlite.org.OpenShared], or any pool with SetMaxOpenConns(1). A private
// [gosqlite.org.OpenInMemory] database gives each connection its own empty
// store, so blobstore writes would appear to vanish — use OpenShared for
// in-memory use.
//
// For write-heavy or streamed objects, consider opening the database with
// synchronous=NORMAL. [gosqlite.org.OpenWAL] leaves SQLite's default (FULL),
// which fsyncs the WAL on every commit — and since each WriteAt is its own
// transaction, that is one fsync per chunk. NORMAL defers the fsync to
// checkpoint, so commits are cheap; in WAL mode it stays crash-consistent (no
// corruption) across application and OS crashes, and only a power loss can drop
// transactions committed since the last checkpoint.
//
// # Bulk and atomic writes
//
// [Store.WriteAt] commits each write in its own transaction. To run many writes
// in one transaction — amortizing the per-write commit (and fsync) for a bulk
// load or stream, and committing them atomically — use [Store.Batch]:
//
//	err := store.Batch(ctx, id, func(w io.WriterAt) error {
//		if _, err := w.WriteAt(head, 0); err != nil {
//			return err
//		}
//		_, err := w.WriteAt(tail, int64(len(head)))
//		return err
//	})
//
// Every write in fn commits together when fn returns nil, or rolls back entirely
// if fn returns an error or panics — a half-written batch never persists. The
// [io.WriterAt] is bound to one transaction (drive it sequentially), and Batch
// holds the write lock for the whole callback, so keep fn tight: buffer a slow
// source first rather than reading it inside. [Store.WriteAtFrom] is the
// convenience form — it copies an [io.Reader] into an object in one Batch.
//
// # Compression
//
// Open a Store with [WithCompression] to store new objects compressed:
//
//	store, _ := blobstore.Open(db, "files", blobstore.WithCompression(blobstore.CompressionDefault))
//
// Each chunk is stored as a whole compressed value (incompressible chunks fall
// back to verbatim, so a chunk is never larger than its payload). Each object
// starts in the Store's mode at Create (override per object with
// [WithObjectCompression]); a Store reads any object regardless of its mode, so
// raw and compressed objects coexist in one store. Sparse holes stay free (an
// unwritten chunk stores nothing and reads as zeros).
//
// Override compression for a single object with [WithObjectCompression] at
// Create — [CompressionNone] stores it raw, any level stores it compressed at
// THAT level, regardless of the Store default. Objects of different modes and
// levels coexist in one store, so you can compress small or cold objects hard
// (e.g. [CompressionBest]) while keeping large or hot ones raw or lightly
// compressed for speed.
//
// [Store.SetCompression] changes an existing object. Changing only the LEVEL of
// a compressed object rewrites nothing — reads are level-agnostic, so an object
// may hold chunks at different levels (e.g. a head at [CompressionBest], an
// appended tail at [CompressionDefault]). Changing the MODE — raw↔compressed,
// including [CompressionNone] to go raw — converts every existing chunk in one
// transaction (an O(object size) pass), so you can compress an object first
// stored raw or decompress one back to in-place random I/O. [Store.Stat] returns
// an object's metadata, including its actual at-rest compression ratio (computed
// from the stored chunk sizes).
//
// The cost: a compressed object can't use in-place incremental BLOB I/O, so
// every operation works on a full chunk in memory — a read decompresses the
// whole chunk, and a partial write read-modify-writes it (a write covering a
// whole chunk skips the read). Compression fits write-once / read-mostly or
// sequentially-streamed compressible data (files, logs, JSON), not hot random
// partial updates or already-compressed payloads. Prefer a larger
// [WithChunkSize] when compressing. Compression composes with page-level
// encryption (gosqlite.org/vfs/crypto): chunks are compressed before the VFS
// encrypts the pages, the correct order.
package blobstore // import "gosqlite.org/blobstore"
