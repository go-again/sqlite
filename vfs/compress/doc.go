// Package compress stores a SQLite database compressed on disk, in two modes.
//
// [Open] is the live mode: it keeps the on-disk file compressed the whole time
// the database is open and queries it in place — a pure-Go, file-backed storage
// engine that translates SQLite's page reads and writes to compressed,
// block-aligned slots in a block-structured container (page directory + block
// allocator + a crash-safe copy-on-write commit), so nothing is ever written to
// disk uncompressed. Durability is per-TRANSACTION: each commit atomically flips
// a ping-pong superblock, so a crash leaves the previous committed state intact
// and SQLite's rollback journal recovers the rest.
//
//	db, err := compress.Open(sqlite.Config{Path: "app.db.az"}, compress.Options{})
//	if err != nil { ... }
//	defer db.Close()
//	// use db exactly like *sql.DB
//
// [Open] supports multiple pooled connections: they share one in-memory
// container and coordinate through the VFS's in-process advisory locks — many
// concurrent readers, one writer at a time. It sets a large page size to match
// the container, disables mmap, and defaults a busy timeout. The default is a
// rollback journal (no uncompressed working set on disk); set Pragmas.JournalMode
// to WAL to opt into WAL mode, where the main DB stays compressed and only the
// transient -wal frames are uncompressed, folded into compressed slots on
// checkpoint. (WAL coordination is in-process only — multiple connections in one
// process, not cross-process.) It refuses a non-container file rather than risk
// clobbering it. [NewVFS] exposes the underlying [VFS] for advanced wiring; most
// callers want [Open].
//
// # Snapshot mode — [OpenSnapshot]
//
// [OpenSnapshot] is the alternative: it inflates the compressed file into a
// private working copy, opens that copy, and recompresses it back over the
// original path at Close — so a single defer db.Close() drains the pool and
// rewrites the compressed file.
//
//	db, err := compress.OpenSnapshot(sqlite.Config{Path: "app.db.az"}, compress.Options{})
//	defer db.Close()
//
// It compresses the database AT REST only; while open it runs from a full,
// uncompressed working copy (under the OS temp dir, or Options.TempDir). Two
// consequences follow, and they are the reason to prefer [Open] for a long-lived
// database:
//
//   - Durability is per-SESSION, not per-transaction. The durable artifact is
//     the snapshot written at Close; a crash while the database is open loses
//     that session's changes (no corruption — the file reverts to its previous
//     Close).
//   - The working copy is plaintext on disk for the lifetime of the handle, so
//     it is NOT a substitute for at-rest encryption.
//
// [OpenSnapshot] fits archival, distribution, backups, and open-modify-close
// tooling. Opening a raw (uncompressed) database with it adopts the file
// (rewritten compressed on Close).
//
// [Pack] and [Unpack] are the same transform without a session — compress an
// existing .db for shipping or storage, and inflate it back.
//
// # Untrusted input
//
// Opening a compressed file from an untrusted source (a downloaded or
// distributed artifact) can inflate a tiny crafted frame into an arbitrarily
// large working copy — a decompression bomb that fills the disk. Set
// [Options.MaxInflatedSize] to cap how much [OpenSnapshot] will inflate, so a
// malformed or hostile file fails instead. [Open] validates its container's
// metadata on open and bounds every page decode, so it rejects a hostile
// container rather than trusting it.
//
// # Compression
//
// The level is chosen with Options.Level (see [Compression]); the zero value
// uses a balanced default. Levels 1–2 are LZ4 (fast), 3–5 are zstd (denser).
// Decoding auto-detects the algorithm, so a file written at one level always
// reads back regardless of the level configured later.
//
// # Encryption at rest
//
// [Open] encrypts the database at rest when [Options.Key] is set: each
// compressed block is encrypted (compress THEN encrypt, so the on-disk bytes are
// both compressed and encrypted), along with the page directory and the
// transient -journal/-wal, reusing the length-preserving cipher of
// [gosqlite.org/vfs/crypto] (Adiantum by default; AES-XTS-256 via
// [Options.Cipher]). The key is the raw cipher key — 32 bytes for Adiantum, 64
// for AES-XTS-256; derive one from a passphrase with
// [gosqlite.org/vfs/crypto.DeriveKey]:
//
//	key, _ := crypto.DeriveKey(passphrase, salt, crypto.Adiantum)
//	db, err := compress.Open(sqlite.Config{Path: "app.db.az"}, compress.Options{Key: key})
//
// Reopening without the key fails with [ErrEncrypted], and with the wrong key
// with [ErrWrongKey]. The guarantee is confidentiality at rest only: like
// vfs/crypto it adds no integrity tag, so the container checksums catch
// accidental corruption but not deliberate tampering, and a passive attacker
// still learns the container geometry and per-page compressed sizes. The
// snapshot [OpenSnapshot] does NOT encrypt — its working copy is plaintext on
// disk — so use live [Open] with a Key for an encrypted database; for a shipped
// artifact, [Pack] output can be piped through any encryptor.
package compress // import "gosqlite.org/vfs/compress"
