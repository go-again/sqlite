// Package compress stores a SQLite database compressed on disk.
//
// [Open] returns a normal [gosqlite.org.DB]: the compressed file at the given
// path is inflated into a private working copy, that copy is opened as an
// ordinary database, and on Close the working copy is compressed back over the
// original path. A single defer db.Close() drains the pool and rewrites the
// compressed file — the same ergonomics as a plain [gosqlite.org.Open] or
// [gosqlite.org/vfs/crypto]'s Open.
//
//	db, err := compress.Open(sqlite.Config{Path: "app.db.az"}, compress.Options{})
//	if err != nil { ... }
//	defer db.Close()
//	// use db exactly like *sql.DB
//
// [Pack] and [Unpack] are the same transform without a session — compress an
// existing .db for shipping or storage, and inflate it back.
//
// [OpenLive] is the live alternative: it keeps the database compressed on disk
// and queries it in place — durable per transaction, never materialising the
// whole database in the clear. Pick between the two with the model below.
//
// # Two models: snapshot ([Open]) vs live ([OpenLive])
//
// [Open] compresses a database AT REST. While it is open the database runs from
// a full, uncompressed working copy (under the OS temp dir, or Options.TempDir);
// the compressed file is (re)written only at Close. Two consequences follow:
//
//   - Durability is per-SESSION, not per-transaction. The durable artifact is
//     the snapshot written at Close. A crash while the database is open leaves
//     the on-disk file at its previous Close — no corruption, but changes made
//     in the interrupted session are lost.
//   - The working copy is the full uncompressed database and exists in
//     plaintext on disk for the lifetime of the handle. So this is NOT a
//     substitute for at-rest encryption.
//
// That makes [Open] a good fit for archival, distribution, backups, and
// open-modify-close tooling over compressible data.
//
// [OpenLive] instead keeps the on-disk file compressed throughout and translates
// SQLite's page reads and writes to compressed, block-aligned slots in a
// block-structured container — a real storage engine (page directory + block
// allocator + a crash-safe copy-on-write commit). Durability is per-TRANSACTION:
// each commit atomically flips a ping-pong superblock, so a crash leaves the
// previous committed state intact and SQLite's rollback journal recovers the
// rest. Nothing is ever written to disk uncompressed. It fits a large,
// compressible database that must stay open continuously and survive crashes.
//
//	db, err := compress.OpenLive(sqlite.Config{Path: "app.db.az"}, compress.Options{})
//	if err != nil { ... }
//	defer db.Close()
//
// [OpenLive] supports multiple pooled connections: they share one in-memory
// container and coordinate through the VFS's in-process advisory locks — many
// concurrent readers, one writer at a time — so a connection pool is safe. It
// uses a rollback journal (it sets a large page size to match the container,
// disables mmap, defaults a busy timeout, and overrides any WAL request). WAL
// needs the shared-memory capability and is not yet implemented; for a
// WAL-mode database use a plain database or the snapshot [Open]. [NewVFS]
// exposes the underlying [LiveVFS] for advanced wiring; most callers want
// [OpenLive].
//
// Opening a raw (uncompressed) database file with [Open] adopts it (rewritten
// compressed on Close); [OpenLive] instead refuses a non-container file rather
// than risk clobbering it.
//
// # Untrusted input
//
// Opening a compressed file from an untrusted source (a downloaded or
// distributed artifact) can inflate a tiny crafted frame into an arbitrarily
// large working copy — a decompression bomb that fills the disk. Set
// [Options.MaxInflatedSize] to cap how much Open will inflate, so a malformed
// or hostile file fails instead.
//
// # Compression
//
// The level is chosen with Options.Level (see [Compression]); the zero value
// uses a balanced default. Levels 1–2 are LZ4 (fast), 3–5 are zstd (denser).
// Decoding auto-detects the algorithm, so a file written at one level always
// reads back regardless of the level configured later.
//
// # Combining with encryption
//
// The snapshot [Open] does not encrypt: its working copy is plaintext, and
// compressing already-encrypted data saves nothing. The live [OpenLive] writes
// only compressed bytes, but does not yet encrypt them either — its
// block-aligned container is designed so per-block encryption can be added in
// the block read/write path (a later increment), giving on-disk bytes that are
// always BOTH compressed and encrypted without VFS chaining. Until then, for a
// shipped artifact, [Pack] output can be piped through any encryptor; for a live
// encrypted database without compression, use [gosqlite.org/vfs/crypto].
package compress // import "gosqlite.org/vfs/compress"
