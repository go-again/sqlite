// Package crypto provides a pure-Go encryption-at-rest VFS for SQLite.
//
// Wrap the default OS VFS with [New], pass the returned name into the
// DSN via `?vfs=<name>`, and SQLite transparently encrypts the main
// database file, rollback journal, WAL frames, temp DB, and sub/temp
// journals at the page boundary. The WAL `-shm` index stays plaintext
// — it's an in-process memory-mapped region (not user data), and the
// WAL path consults it via xShmMap rather than xRead/xWrite. Match
// PageSize to the database's `PRAGMA page_size` (default 4096).
//
// Cipher choice: Adiantum (default, 32-byte key, length-preserving
// wide-block construction from lukechampine.com/adiantum) or
// AES-XTS-256 (64-byte key = two AES-256 keys, golang.org/x/crypto/xts).
// Both modes encrypt in place; on-disk file size matches plaintext.
// Pick AES-XTS only when a compliance regime requires AES; otherwise
// Adiantum is the better default — wide-block (full-page corruption
// on tamper), faster on CPUs without AES-NI, no AES side-channel
// surface to reason about.
//
// # Lifecycle
//
// Call [New] once per process per encryption configuration. The
// returned *FS owns the cipher state and an underlying libc TLS;
// call [FS.Close] only AFTER every sql.DB / sql.Conn that opened
// against this VFS has been closed. Closing the VFS while a query
// is still in flight is undefined behavior — the io-method
// trampolines hold a reference to the cipher that the GC won't
// reclaim until the FS itself becomes unreachable, but the unregister
// call invalidates the VFS slot in SQLite's global table, so the next
// trampoline invocation against this VFS will see corrupted struct
// pointers. Order shutdowns: drain DBs first, then Close the FS.
//
// # Crypto contract
//
//   - Confidentiality at rest only. No MAC, no SQLCipher format
//     compatibility, no integrity tag. An attacker with passive read
//     access to disk recovers nothing without the key.
//   - No integrity / no tamper detection. A write-capable attacker can
//     flip ciphertext bytes; SQLite sees corruption (page checksum
//     failure, header parse error). Pair with disk-level integrity
//     (LUKS dm-integrity, ZFS checksums) if active tampering is in scope.
//   - Cross-file substitution between distinct file KINDS is detected:
//     the tweak includes the file-kind tag (main DB / journal / WAL /
//     temp / sub-journal), so a ciphertext block from one file kind
//     does not decrypt cleanly when copied into another at the same
//     offset. See cipher.go's file-kind enum for the wire values.
//   - Within-kind substitution between distinct files of the SAME
//     kind is undetected. SQLite can open multiple concurrent temp
//     DBs / sub-journals (e.g. during a query with multiple sort
//     spills), all sharing the same kind byte and the same key — so
//     a tweak `(kind, pageNum)` repeats across the temp-class files.
//     Cross-temp swap by an offline attacker decrypts cleanly. Within
//     the engine SQLite is strict about file-handle identity, so
//     runtime confusion is not a concern; this matters only for
//     forensic-byte-swap attackers.
//   - Within-file replay IS undetected: an older `-wal` byte range
//     swapped over a newer one at the same offset (same file kind,
//     same page number) decrypts cleanly. Inherent to length-
//     preserving disk encryption with no authenticated counter.
//   - Adiantum vs AES-XTS corruption granularity: a single ciphertext
//     bit flip under Adiantum (wide-block) garbles the entire
//     decrypted page — SQLite's header parser fails fast. The same
//     flip under AES-XTS (narrow 16-byte blocks) corrupts only the
//     affected XTS block; the rest of the page decrypts intact and
//     may be accepted by SQLite if the corruption lands outside the
//     header / checksum region. Pair AES-XTS with disk-level
//     integrity (LUKS dm-integrity, ZFS) if active tampering is in
//     scope.
//   - Key is the caller's problem. We treat the byte slice as opaque
//     and derive nothing from it. Use argon2id / scrypt for passphrase
//     derivation; we don't ship one.
//   - PageSize MUST match the database's `PRAGMA page_size`. Mismatch
//     scrambles every read — SQLite reports "file is not a database"
//     or similar within ~one query.
//
// # Drift discipline
//
// This package reaches into modernc.org/sqlite/lib's exported
// Tsqlite3_vfs / Tsqlite3_io_methods struct types via field-by-field
// copies (never memcpy) so a future modernc bump that reorders fields
// fails to compile rather than silently scrambling layout. The same
// libc-version-pin discipline that CLAUDE.md describes for conn.go,
// vtab.go, etc. applies here: bumping modernc.org/sqlite without re-
// transpiling may require fixing field assignments in crypto.go's
// [New].
//
// # On-disk format
//
// Length-preserving page-level encryption. No header, no magic bytes,
// no per-page IV or MAC: the on-disk byte count equals plaintext, and
// every block looks like uniform random data to an attacker without
// the key. The tweak fed to the cipher mixes the page number with a
// 1-byte file-kind tag (main DB / journal / WAL / temp / sub-journal;
// see the FileKind* constants in cipher.go) so a ciphertext page
// from -wal does not decrypt to the same plaintext when copied into
// the main DB at the same offset.
//
// The file-kind byte is part of the on-disk format. Databases
// written by a build that predates the file-kind tweak are not
// readable by this package, and vice versa. There's no version
// banner in the file itself — SQLite reports "file is not a
// database" when the cipher decrypts garbage. If you have an
// archived pre-format-break encrypted DB, decrypt it with the older
// build into plaintext first and re-encrypt with the current one.
//
// # Threat model boundaries
//
// In scope:
//   - Plaintext recovery from a stolen disk / unencrypted backup.
//   - Forensic byte-level inspection of database files.
//
// Out of scope:
//   - Live process memory: keys, decrypted pages, prepared statement
//     parameters all exist in RAM unprotected.
//   - Active tampering and rollback to an older valid state.
//   - Side channels (timing, cache).
//   - Key derivation / rotation / storage. The package treats
//     [Options.Key] as opaque material; how you get it there and
//     dispose of it is your concern.
//
// # Key rotation recipe
//
// We don't ship a rotation API because rotation is a row-level
// operation under the covers, not a cipher one. The portable recipe:
//
//  1. Open the source DB with [New] using the old key.
//  2. Open a fresh destination DB with [New] using the new key.
//  3. Stream rows from source to destination (`INSERT INTO dest …
//     SELECT … FROM source`, in a single transaction per table, or
//     via the standard sqlite3 `.dump` / `.read` if you prefer SQL).
//  4. Close both, atomically rename the destination over the source
//     (`os.Rename` is atomic on the same filesystem).
//  5. Have all consumers reopen with the new key.
//
// This pattern also works for cipher rotation (e.g. Adiantum →
// AES-XTS) and for migrating from a pre-format-break encrypted DB
// to the current file-kind-tweaked format.
//
// # Observability
//
// Pass a [Recorder] via [Options.Recorder] to receive one event per
// xRead / xWrite trampoline invocation, tagged with the file kind so
// dashboards can split metrics per main-DB / journal / WAL / temp.
// [NewSlogRecorder] is the built-in recorder that emits one slog
// record per op (Debug-level for normal-path ops and SHORT_READ;
// Warn-level for anything else). [FileKindName] turns the file-kind
// byte into a stable human-readable string for log/metric labels.
//
// Shape difference from [github.com/go-again/sqlite/vec.Recorder] and
// [github.com/go-again/sqlite/fts.Recorder]: those packages expose
// Recorder via a `Wrap(table, WithRecorder(...))` decorator because
// callers wrap individual Table / Index handles. vfs/crypto registers
// a VFS once at boot, so Recorder lives on Options instead. The
// Recorder method shape also drops the ctx argument (VFS trampolines
// fire from transpiled C with no Go-side ctx in scope) and surfaces
// an int32 rc instead of a Go error (SQLite returns result codes,
// not Go errors). Both differences are intentional, not divergence.
//
// # See also
//
//   - examples/vfs-crypto — runnable end-to-end demo.
//   - [github.com/go-again/sqlite/vec] / [github.com/go-again/sqlite/fts]
//     — both compose with this VFS transparently. The same Recorder-
//     shaped observability surface is parallel across all three.
package crypto
