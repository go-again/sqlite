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

## Compressed (and optionally encrypted) at rest

```go
import "gosqlite.org/vfs/compress"
db, _ := compress.Open(sqlite.Config{Path: "app.db.az"}, compress.Options{Key: key}) // Key optional
defer db.Close()
```

Live `compress.Open` keeps the database compressed on disk the whole time and queries it in place — durable **per-transaction**, multiple pooled connections, rollback journal by default (WAL opt-in). Pass `Options.Key` to also **encrypt it at rest** — each compressed block plus the directory and the `-journal`/`-wal` (compress then encrypt), reusing `vfs/crypto`'s cipher (Adiantum or AES-XTS; derive a key with `crypto.DeriveKey`). Wrong key → `compress.ErrWrongKey`, missing key → `compress.ErrEncrypted`; confidentiality at rest only (no integrity tag).

For **several people to open one database** without sharing a secret, pass `Options.Recipients` instead of `Options.Key` — a random data key wrapped per recipient (an SSH key, a passphrase, or an age recipient via `gosqlite.org/crypto/keyring`) into a keyslot; reopen with `Options.Identities` (none matching → `compress.ErrNoIdentity`). Change the set on a closed database with `compress.Rewrap(path, by, writeAs, keyring.Membership{...})` or `compress.Rekey(...)` (re-encrypt under a fresh key, true revocation).

To restrict who may change membership, pin **masters** (ed25519 keys) with `Options.Masters` + `Options.SignWith`: the keyslot is signed, only a master may `Rewrap`/`Rekey` (`compress.ErrNotMaster` otherwise), and readers that pin the masters they trust reject any membership not signed by one (`compress.ErrUnauthorized`).

For **read-only recipients**, pin **writers** with `Options.Writers` (requires `Masters`): commits are writer-signed and slots carry crypto hashes (authenticated mode — the one mode that adds integrity), so a non-writer can read/verify but not write. A connection with `Options.WriteAs` may write; without it the handle is read-only (`compress.ErrReadOnlyRecipient`), and a forged or tampered state is rejected (`compress.ErrUnauthorized`). Remove a writer/master via `Rekey`.

`compress.OpenSnapshot` is the alternative: it inflates the file into a temp working copy for the session and recompresses on `Close` — durability **per-session**, and the working copy is **plaintext** (not encrypted), so prefer live `Open` (with a `Key`) for a long-lived or encrypted database. `compress.Pack(dst, src, level)` / `compress.Unpack(dst, src)` do the same transform on a `.db` without a session. Own module (`gosqlite.org/vfs/compress`). Fits archival / distribution / open-modify-close over compressible data.

To back a WRITABLE database with your own Go storage, see the `custom-vfs` skill. Full reference: [`docs/guides/encryption.md`](../../docs/guides/encryption.md), [`docs/guides/compression.md`](../../docs/guides/compression.md), and siblings.
