// Package vault stores a SQLite database in a block-structured container on
// disk, where compression and encryption are independent options.
//
// The container is a pure-Go, file-backed storage engine: it translates SQLite's
// page reads and writes to block-aligned slots (page directory + block allocator
// + a crash-safe copy-on-write commit). Each page can be stored raw or
// compressed, in the clear or encrypted, so a database may be plain, compressed,
// encrypted, or both — four combinations from one format. The default
// (Options{}) is a plain container. Set [Options.Level] to compress (see
// [Compression]); set [Options.Key] or [Options.Recipients] to encrypt (see
// Encryption at rest, below); set both for both.
//
// [Open] is the live mode: it keeps the on-disk file in container form the whole
// time the database is open and queries it in place, so nothing is ever written
// to disk in a weaker form than configured — never uncompressed when compression
// is on, never plaintext when a key is set. Durability is per-TRANSACTION: each
// commit atomically flips a ping-pong superblock, so a crash leaves the previous
// committed state intact and SQLite's rollback journal recovers the rest.
//
//	db, err := vault.Open(sqlite.Config{Path: "app.db"}, vault.Options{}) // plain container
//	if err != nil { ... }
//	defer db.Close()
//	// use db exactly like *sql.DB
//
// [Open] supports multiple pooled connections: they share one in-memory
// container and coordinate through the VFS's in-process advisory locks — many
// concurrent readers, one writer at a time. It sets a large page size to match
// the container, disables mmap, and defaults a busy timeout. The default is a
// rollback journal (no uncompressed working set on disk); set Pragmas.JournalMode
// to WAL to opt into WAL mode, where the main DB stays in container form and only
// the transient -wal frames are written plainly, folded back into container slots
// on checkpoint. (WAL coordination is in-process only — multiple connections in one
// process, not cross-process.) It refuses a non-container file rather than risk
// clobbering it. [NewVFS] exposes the underlying [VFS] for advanced wiring; most
// callers want [Open].
//
// # Snapshot mode — [OpenSnapshot]
//
// [OpenSnapshot] is the alternative: it inflates the file (compressed or raw) into
// a private working copy, opens that copy, and repacks it back over the original
// path at Close — so a single defer db.Close() drains the pool and rewrites the
// file.
//
//	db, err := vault.OpenSnapshot(sqlite.Config{Path: "app.db"}, vault.Options{Level: vault.CompressionDefault})
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
// [Compact] reclaims space on a closed database: under churn the container reuses
// freed blocks (the at-rest file plateaus rather than growing), and Compact rewrites
// it into a fresh, densely-packed file — returning the freed blocks to the OS —
// while continuing the commit generation so an [Options.Anchor] stays valid.
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
// Compression is optional and chosen with Options.Level (see [Compression]). The
// zero value ([CompressionNone]) is off — pages are stored raw. Set a level to
// compress: levels 1–2 are LZ4 (fast), 3–5 are zstd (denser). Compression is
// per-page, so a level applies to pages written under it; decoding auto-detects
// the algorithm, so a file written at one level (or raw) always reads back
// regardless of the level configured later.
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
//	db, err := vault.Open(sqlite.Config{Path: "app.db"}, vault.Options{Key: key})
//
// To let several parties open one database, each with their own key and no shared
// secret, set [Options.Recipients] instead of [Options.Key]: a random data key
// encrypts the container and is wrapped once per recipient — an SSH key, a
// passphrase, or an age recipient built with [gosqlite.org/crypto/keyring] — into
// a keyslot any one of them can open. Reopen with [Options.Identities]; the first
// identity that matches a keyslot unlocks the database. Key and Recipients are
// mutually exclusive and set only at create time.
//
//	alice, _ := keyring.SSHRecipient(alicePubKey)
//	bob, _ := keyring.SSHRecipient(bobPubKey)
//	db, err := vault.Open(sqlite.Config{Path: "app.db"}, vault.Options{Recipients: []keyring.Recipient{alice, bob}})
//
// The recipient set changes on a closed database without re-encrypting via
// [Rewrap] (rewrap the data key to a new set — O(1), access-list management) or,
// for true cryptographic revocation, [Rekey] (re-encrypt under a fresh data key —
// O(database size)).
//
// By default every recipient is an administrator (any of them can [Rewrap]). To
// restrict that, pin one or more masters with [Options.Masters] (ed25519 keys, plus
// [Options.SignWith] at create): the keyslot's membership is then signed, and only
// a master can add or remove recipients and masters. A reader enforces this by
// pinning the masters it trusts in [Options.Masters] at open — a membership not
// signed by a trusted master is rejected with [ErrUnauthorized], and a non-master
// that tries to administer gets [ErrNotMaster]. Removing a master means [Rekey] by
// another master (a fresh data key, so the removed party — and any rolled-back old
// keyslot — can read nothing).
//
//	master, masterID, _ := keyring.GenerateMaster()
//	db, _ := vault.Open(cfg, vault.Options{Masters: []keyring.MasterRecipient{master}, SignWith: masterID, Recipients: []keyring.Recipient{alice}})
//	// later, on the closed database, only a master may change membership:
//	err := vault.Rewrap(path, masterID, nil, keyring.Membership{Masters: []keyring.MasterRecipient{master}, Members: []keyring.Recipient{alice, bob}})
//
// # Tamper-evidence (authenticated mode)
//
// By default encryption gives confidentiality only: a tampered or rolled-back
// container still opens (the checksums catch accidental corruption, not deliberate
// edits). Authenticated mode adds integrity — every commit carries a keyed proof
// over the committed state and each slot a hash, so a modified, truncated, or
// partially-rolled-back container (one block old, the rest new) fails to open with
// [ErrTampered]. There are two flavours, by who the attacker is.
//
// Set [Options.Authenticate] for the symmetric flavour: the root proof is an HMAC
// keyed by a key derived from the data key, so any holder of the data key can both
// write and verify. It protects against an attacker WITHOUT the key — modification
// or a partial/inconsistent rollback is detected and rejected with [ErrTampered] —
// and needs no extra keys (it requires only encryption, Key or Recipients). A holder
// cannot strip it: the authenticated flag lives in the signed state, so downgrading a
// non-authenticated container to authenticated, or vice versa, is rejected.
//
// It is tamper-evident; rollback resistance is OPTIONAL. The signed root binds the
// commit generation, so an attacker cannot renumber a state — but a complete, self-
// consistent earlier committed image (every block from one past generation) is still
// validly signed. Supply [Options.Anchor] — an external monotonic counter kept
// OUTSIDE the file (a TPM/keystore counter, or [FileAnchor] on separate storage) —
// to reject such a rollback with [ErrRolledBack]: each commit records its generation
// in the anchor, and open refuses a generation below the recorded floor. Without an
// anchor, [Rekey] is the durable revocation path (a fresh data key, so a rolled-back
// snapshot's data and stale keyslot can no longer be read).
//
//	db, err := vault.Open(cfg, vault.Options{Key: key, Authenticate: true})
//
// For the asymmetric flavour — where a holder of the read key must NOT be able to
// forge a write others accept — pin one or more writers with [Options.Writers]
// (ed25519 keys; requires Masters). Every commit is then signed by a writer, so a
// recipient that is not a writer can read and verify but cannot produce a write
// others accept: it is read-only. A connection opened with a writer identity
// ([Options.WriteAs]) may write; without one it is read-only and the VFS refuses
// writes with [ErrReadOnlyRecipient]. A reader pins [Options.Masters] (the trust
// anchor that authorizes the writer list); a state not signed by an authorized
// writer, or a tampered slot, is rejected with [ErrUnauthorized]. The writer set is
// changed by a master via [Rewrap]/[Rekey] (which re-sign the state); remove a
// writer or master with [Rekey].
//
// Reopening without a key or matching identity fails with [ErrEncrypted], with
// the wrong raw key with [ErrWrongKey], and with no matching identity with
// [ErrNoIdentity]. Outside authenticated mode the guarantee is confidentiality at
// rest only: like vfs/crypto it adds no integrity tag, so the container checksums
// catch accidental corruption but not deliberate tampering, and a passive attacker
// still learns the container geometry and per-page compressed sizes. The
// snapshot [OpenSnapshot] does NOT encrypt — its working copy is plaintext on
// disk — so use live [Open] with a Key for an encrypted database; for a shipped
// artifact, [Pack] output can be piped through any encryptor.
package vault // import "gosqlite.org/vfs/vault"
