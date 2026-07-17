// Package blobstore stores large, growable, randomly-writable byte objects
// in SQLite with O(chunk) memory — never materializing a whole object.
//
// SQLite's incremental BLOB I/O ([gosqlite.org.Conn.OpenBlob]) is fixed-size:
// a value cannot grow once allocated, and the usual "grow with
// `col || zeroblob(delta)`" trick silently truncates (SQLite drops zeroblob
// operands under `||`). blobstore is the supported answer for an
// unknown-or-growing size: it manages the block and chunk tables for you and
// exposes each object as an [io.ReaderAt] / [io.WriterAt], filling the gap that
// every "put a file in SQLite" app otherwise re-derives by hand.
//
// # Model
//
// Each object is a sequence of fixed-size chunks, and each chunk maps to a
// block — the reference-counted row that actually holds its bytes. A block is
// allocated full (via zeroblob, the one correct use) the first time any byte in
// its chunk is written, then written in place with incremental BLOB I/O — so a
// value never grows and the zeroblob/`||` trap never arises. Growth is just new
// chunk and block rows. The object's logical size is authoritative: reads clamp
// to it, and a chunk that was never written reads back as zeros (sparse holes
// are free). When a block is referenced by more than one chunk it is copied
// before an in-place write (copy-on-write), so chunks that share a block stay
// independent — the foundation for cheap whole-object copies.
//
// # Usage
//
//	store, err := blobstore.Open(db, "files")           // creates files_objects, files_blocks, files_chunks
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
//	store.Delete(ctx, id)                                // frees the blocks it alone holds
//
// Open creates four tables from the name you pass — "<name>_objects" (one row
// per object: id, logical size, chunk size, mode, retention, creation time),
// "<name>_blocks"
// (the reference-counted block data), "<name>_chunks" (the (object, sequence) ->
// block mapping), and "<name>_versions" (point-in-time snapshots). The name is
// validated as a SQL identifier. All of them share whatever database the
// [gosqlite.org.DB] points at; put other application tables in the same database
// freely. [OpenReadOnly] reattaches to an already-provisioned store without
// issuing any DDL, so it works against a read-only database (snapshot browsing,
// an image on read-only media) and refuses every write with [ErrReadOnly].
//
// # Sharing: clone, versions, dedup
//
// Because chunk bytes live in reference-counted blocks, several objects can
// share content with no copy. [Store.Clone] makes a new object identical to an
// existing one in O(metadata) — it copies the mapping and bumps refcounts, never
// the bytes — and the two diverge copy-on-write as either is written.
// [Store.Stat] reports the split as UniqueBytes (blocks this object alone holds,
// reclaimed if it is deleted) and SharedBytes (blocks held in common with a
// clone or version).
//
// A version is a copy-on-write snapshot of an object's content: [Store.NewVersion]
// records one (reusing Clone, so it is O(metadata) and shares all blocks with the
// live object until it diverges), [Store.ListVersions] enumerates them,
// [Store.OpenVersion] reads one back immutably. A per-object retention [Policy]
// (set with [WithObjectVersioning] or [Store.SetRetention]) bounds how many or
// how old versions are kept; [Store.Prune] and the sweep after each NewVersion
// enforce it, freeing the blocks a dropped version alone held.
//
// [WithDedup] turns on content-addressed deduplication: a full-block write whose
// bytes match an already-stored block references that block instead of writing a
// copy, deduplicating identical content across objects at the cost of a content
// hash per write.
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
// # Joining a caller's transaction
//
// [Store.Batch] opens its own transaction. To instead fold object writes into a
// LARGER application transaction — so content commits atomically with the caller's
// own rows and shares one writer — use [Store.OnConn], which runs on a connection
// the caller already holds: because a SQLite transaction is per-connection, writes
// on that connection join whatever transaction is open on it.
//
//	conn, _ := db.Conn(ctx)          // one connection the caller owns
//	defer conn.Close()
//	conn.ExecContext(ctx, "BEGIN IMMEDIATE")
//	cs := store.OnConn(conn)
//	id, _ := cs.Create(ctx)                       // joins the caller's transaction
//	cs.WriteAt(ctx, id, content, 0)               // ...as does the content
//	conn.ExecContext(ctx, "INSERT INTO inode(...) VALUES(...)", id) // and the caller's row
//	conn.ExecContext(ctx, "COMMIT")               // all commit together
//
// The caller owns BEGIN/COMMIT/ROLLBACK; [ConnStore] opens no transaction of its
// own, and a read through it sees the transaction's own not-yet-committed writes.
// It is the way for, e.g., a filesystem holding one long-lived writer to store
// file content without a flush-around-blobstore seam or a second writer contending
// for the write lock. The post-delete / post-truncate incremental vacuum is the
// caller's to run after commit (it cannot run inside an open transaction).
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
//
// [Compress] and [Decompress] expose the store's codec standalone, for a caller
// that keeps tiny objects OUTSIDE the chunk store (e.g. inlined in its own table
// row) but wants the exact same on-disk encoding — incompressible input falls back
// to verbatim, and Decompress bounds its output as a decompression-bomb guard.
package blobstore // import "gosqlite.org/blobstore"
