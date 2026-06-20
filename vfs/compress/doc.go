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
// # Model: snapshot, not live
//
// This package compresses a database AT REST. While the database is open it
// runs from a full, uncompressed working copy (under the OS temp dir, or
// Options.TempDir); the compressed file is (re)written only at Close. Two
// consequences follow, and they are the reason to reach for this over a live
// compressing VFS:
//
//   - Durability is per-SESSION, not per-transaction. The durable artifact is
//     the snapshot written at Close. A crash while the database is open leaves
//     the on-disk file at its previous Close — no corruption, but changes made
//     in the interrupted session are lost. (A plain database, or an encrypting
//     VFS, is durable per committed transaction.)
//   - The working copy is the full uncompressed database and exists in
//     plaintext on disk for the lifetime of the handle. So this is NOT a
//     substitute for at-rest encryption.
//
// That makes it a good fit for archival, distribution, backups, and
// open-modify-close tooling over compressible data — and a poor fit for a
// large database that must stay open continuously or be durable across a crash
// mid-session.
//
// Opening a raw (uncompressed) database file with [Open] adopts it: the file
// is rewritten compressed on Close.
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
// At-rest encryption of the compressed artifact is not built in here: because
// the working copy is plaintext, encrypting it live would have to happen
// underneath this package, and compressing already-encrypted data saves
// nothing. Transparent, per-transaction compression AND encryption together —
// where the on-disk bytes are always both — is the job of a live compressing
// VFS composed with [gosqlite.org/vfs/crypto]; it is planned separately. For a
// shipped artifact, [Pack] output can be piped through any encryptor.
package compress // import "gosqlite.org/vfs/compress"
