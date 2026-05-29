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
// # See also
//
//   - examples/vfs-crypto — runnable end-to-end demo.
//   - plan-vfs-crypto.md — architecture rationale and phase plan.
//   - plan-audit-vfs-crypto.md — outstanding follow-up work.
package crypto
