---
name: encryption-and-vfs
description: Use when the task needs encryption at rest, page-checksum corruption detection, an in-memory database with isolation, or serving a database from an embed.FS / byte buffer in a Go app with go-again/sqlite.
---

# Encryption, checksums & in-memory VFSes

All of these are VFSes registered under a name, referenced from the DSN via `?vfs=<name>`. Keep the returned `*FS` handle alive and `defer fs.Close()`.

## Encryption at rest

```go
import "github.com/go-again/sqlite/vfs/crypto"

key := make([]byte, 32) // Adiantum: 32 bytes; AES-XTS-256: 64 bytes. Derive from passphrase/keyring.
name, fs, _ := crypto.New(crypto.Options{Key: key})
defer fs.Close()
db, _ := sql.Open("sqlite", "file:secret.db?vfs="+name)
```

Or via Config: `sqlite.Open(sqlite.Config{Path: "secret.db", Encryption: &sqlite.Encryption{Key: key}})`. Confidentiality only — no SQLCipher format compatibility, no MAC. Cipher: `crypto.Options{Cipher: crypto.AESXTS}` (default Adiantum).

## Checksums (corruption detection)

```go
import "github.com/go-again/sqlite/vfs/cksm"
name, fs, _ := cksm.New(cksm.Options{}); defer fs.Close()
db, _ := sql.Open("sqlite", "file:db.db?vfs="+name)
sc, _ := db.Conn(ctx)
sc.Raw(func(d any) error { return d.(*sqlite.Conn).EnableChecksums("main") })
```

A flipped bit then surfaces as `SQLITE_IOERR_DATA` on read. Stack under crypto via `crypto.Options{WrapVFS: cksmName}` for checksum-then-encrypt.

## In-memory

```go
import "github.com/go-again/sqlite/vfs/mvcc"  // snapshot isolation
import "github.com/go-again/sqlite/vfs/memdb" // direct, no MVCC
name, fs, _ := mvcc.New(mvcc.Options{}); defer fs.Close()
db, _  := sql.Open("sqlite", "file:/shared.db?vfs="+name)  // SHARED (leading slash)
db2, _ := sql.Open("sqlite", "file:scratch.db?vfs="+name)  // PRIVATE (no slash)
```

For a simple shared in-memory test without a VFS, `sqlite.OpenShared(name)` is the one-liner.

## embed.FS / byte buffer (read-only)

```go
import "github.com/go-again/sqlite/vfs"
//go:embed seed.db
var seed embed.FS
name, _, _ := vfs.New(seed)
db, _ := sql.Open("sqlite", "file:seed.db?vfs="+name+"&mode=ro")
// or from a []byte: vfs.NewReader(bytes.NewReader(bs), int64(len(bs))) — file is named "db"
```

To back a WRITABLE database with your own Go storage, see the `custom-vfs` skill. Full reference: [`docs/guides/encryption.md`](../../docs/guides/encryption.md) and siblings.
