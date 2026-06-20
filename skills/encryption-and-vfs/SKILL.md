---
name: encryption-and-vfs
description: Use when the task needs encryption at rest, page-checksum corruption detection, an in-memory database with isolation, or serving a database from an embed.FS / byte buffer in a Go app with gosqlite.
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

To back a WRITABLE database with your own Go storage, see the `custom-vfs` skill. Full reference: [`docs/guides/encryption.md`](../../docs/guides/encryption.md) and siblings.
