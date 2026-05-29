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
//   - Cross-file substitution IS detected at the cipher level: the
//     tweak includes the file kind (main DB / journal / WAL / temp /
//     sub-journal), so a ciphertext block from one file kind does not
//     decrypt cleanly when copied into another at the same offset.
//     See cipher.go's file-kind enum for the wire values.
//   - Within-file replay IS undetected: an older `-wal` byte range
//     swapped over a newer one at the same offset (same file kind,
//     same page number) decrypts cleanly. Inherent to length-
//     preserving disk encryption with no authenticated counter.
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
// see the fileKind* constants in cipher.go) so a ciphertext page
// from -wal does not decrypt to the same plaintext when copied into
// the main DB at the same offset.
//
// The file-kind byte is part of the on-disk format. Databases
// written by a build that predates the file-kind tweak (the
// pre-v0.5 development series before this format break landed) are
// not readable by this package, and vice versa. There's no version
// banner in the file itself — SQLite reports "file is not a
// database" when the cipher decrypts garbage. If you have an
// archived pre-format-break encrypted DB, decrypt it with the older
// package version into plaintext first and re-encrypt with the
// current one.
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
//   - plan-vfs-crypto.md — architecture rationale and phase plan.
//   - plan-audit-vfs-crypto.md — outstanding follow-up work.
package crypto
