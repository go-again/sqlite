---
name: encryption-and-vfs
description: Use when the task needs encryption at rest, storing a database compressed at rest, page-checksum corruption detection, an in-memory database with isolation, or serving a database from an embed.FS / byte buffer in a Go app with gosqlite.
---

# Encryption, checksums & in-memory VFSes

All of these are VFSes registered under a name, referenced from the DSN via `?vfs=<name>`. Keep the returned `*FS` handle alive and `defer fs.Close()`.

## Encryption at rest

```go
import "gosqlite.org/vfs/crypto"

key := make([]byte, 32) // Adiantum: 32 bytes; AES-XTS-256: 64 bytes. Derive from passphrase/keyring.
name, fs, _ := crypto.New(crypto.Options{Key: key})
defer fs.Close()
db, _ := sql.Open("sqlite", "file:secret.db?vfs="+name)
```

Or in one call via `crypto.Open` (registers the VFS, routes the Config through it, and bundles teardown into `db.Close()`): `crypto.Open(sqlite.Config{Path: "secret.db"}, crypto.Options{Key: key})`. Confidentiality only — no SQLCipher format compatibility, no MAC. Cipher: `crypto.Options{Cipher: crypto.AESXTS}` (default Adiantum). `vfs/crypto` is its own module (`gosqlite.org/vfs/crypto`).

`vfs/crypto` is confidentiality-only with a single raw `Options.Key`. For **multiple recipients** (a wrapped data key, no shared secret), crash-safe key rotation, or tamper-evident storage (per-page integrity + a signed root), use `gosqlite.org/vfs/vault` — a self-contained container that keeps the keyslot inside the file, where compression and encryption are independent options: encrypt, compress, or both. Tamper-evidence comes in two flavours — symmetric (`Options.Authenticate`, keyed by the data key) and writer-signed (`Options.Writers`, ed25519, for read-only recipients). For full rollback resistance set `Options.Anchor` (an external monotonic counter kept off the file — a TPM/keystore, or `vault.FileAnchor` on separate storage): open rejects a generation below the recorded floor with `vault.ErrRolledBack`. `vault.Compact` reclaims space on a closed, churned database (rewrites it densely, continuing the generation so the anchor stays valid).

## Checksums (corruption detection)

```go
import "gosqlite.org/vfs/cksm"
name, fs, _ := cksm.New(cksm.Options{}); defer fs.Close()
db, _ := sql.Open("sqlite", "file:db.db?vfs="+name)
sc, _ := db.Conn(ctx)
sc.Raw(func(d any) error { return d.(*sqlite.Conn).EnableChecksums("main") })
```

A flipped bit then surfaces as `SQLITE_IOERR_DATA` on read. Stack under crypto via `crypto.Options{WrapVFS: cksmName}` for checksum-then-encrypt.

## In-memory

```go
import "gosqlite.org/vfs/mvcc"  // snapshot isolation
import "gosqlite.org/vfs/memdb" // direct, no MVCC
name, fs, _ := mvcc.New(mvcc.Options{}); defer fs.Close()
db, _  := sql.Open("sqlite", "file:/shared.db?vfs="+name)  // SHARED (leading slash)
db2, _ := sql.Open("sqlite", "file:scratch.db?vfs="+name)  // PRIVATE (no slash)
```

For a simple shared in-memory test without a VFS, `sqlite.OpenShared(name)` is the one-liner.

## embed.FS / byte buffer (read-only)

```go
import "gosqlite.org/vfs"
//go:embed seed.db
var seed embed.FS
name, _, _ := vfs.New(seed)
db, _ := sql.Open("sqlite", "file:seed.db?vfs="+name+"&mode=ro")
// or from a []byte: vfs.NewReader(bytes.NewReader(bs), int64(len(bs))) — file is named "db"
```

## Container at rest — compression + encryption (`vfs/vault`)

```go
import "gosqlite.org/vfs/vault"
db, _ := vault.Open(sqlite.Config{Path: "app.db"}, vault.Options{Level: vault.CompressionDefault, Key: key}) // both optional
defer db.Close()
```

`vfs/vault` is a block-structured container where **compression and encryption are independent options** — plain, compressed, encrypted, or both. Live `vault.Open` keeps the database in container form on disk and queries it in place — durable **per-transaction**, multiple pooled connections, rollback journal by default (WAL opt-in). Set `Options.Level` to compress; set `Options.Key` to encrypt each block + the directory + `-journal`/`-wal` (compress THEN encrypt), reusing `vfs/crypto`'s cipher (Adiantum or AES-XTS; derive a key with `crypto.DeriveKey`). Wrong key → `vault.ErrWrongKey`, missing key → `vault.ErrEncrypted`; by default confidentiality at rest only.

For **several people to open one database** without sharing a secret, pass `Options.Recipients` instead of `Options.Key` — a random data key wrapped per recipient (an SSH key, a passphrase, or an age recipient via `gosqlite.org/crypto/keyring`; `keyring.ParseAuthorizedKeys` parses a whole `authorized_keys` file) into a keyslot; reopen with `Options.Identities` (none matching → `vault.ErrNoIdentity`). Change the set on a closed database with `vault.Rewrap(...)` or `vault.Rekey(...)` (re-encrypt under a fresh key, true revocation). Pin **masters** (ed25519) with `Options.Masters` + `Options.SignWith` so only a master may `Rewrap`/`Rekey` (`vault.ErrNotMaster` otherwise) and readers reject a membership not signed by a trusted master (`vault.ErrUnauthorized`). An admin lists the full current membership with `vault.Members(path, by)` — master-only (the member list is sealed to the masters inside the keyslot).

**Tamper-evidence:** set `Options.Authenticate` for a symmetric MAC'd root (keyed by the data key — integrity vs an attacker WITHOUT the key), or for **read-only recipients** pin `Options.Writers` + `WriteAs` (ed25519, requires `Masters`) so a non-writer can read/verify but not write (`vault.ErrReadOnlyRecipient`); a tampered or partially-rolled-back container fails to open (`vault.ErrTampered`). For full **rollback resistance**, add `Options.Anchor` — an external monotonic counter kept off the file (a TPM/keystore, or `vault.FileAnchor` on separate storage); a rolled-back image is rejected with `vault.ErrRolledBack`.

`vault.Compact(cfg, opts)` reclaims space on a closed, churned database (rewrites it densely, returning freed blocks to the OS, continuing the generation so an anchor stays valid); `vault.Trim(path, maxBytes)` is the online counterpart — it returns trailing free blocks to the OS while the database stays open (tail-only, a cheap truncate; `Compact` stays the densest reclaim). `vault.Snapshot(dst, src, readOpts, writeOpts)` writes a consistent encrypted+compressed copy to a NEW path, optionally re-sealed to a different recipient set, with no plaintext on disk. `vault.OpenSnapshot` inflates the file into a temp working copy for the session and repacks on `Close` — durability **per-session**, working copy **plaintext** (use live `Open` for an encrypted database); `vault.Pack`/`vault.Unpack` do the same transform on a `.db` without a session. Own module (`gosqlite.org/vfs/vault`).

To back a WRITABLE database with your own Go storage, see the `custom-vfs` skill. Full reference: [`docs/guides/vault.md`](../../docs/guides/vault.md) (the container), [`docs/guides/encryption.md`](../../docs/guides/encryption.md) (`vfs/crypto`), and siblings.
