---
title: Encryption at rest
description: Pure-Go page-level encryption VFS — Adiantum or AES-XTS-256, with a slog recorder and checksum stacking.
sidebar:
  order: 8
---

# Encryption at rest

`vfs/crypto` is a pure-Go, page-level encryption VFS — Adiantum (default, 32-byte key) or AES-XTS-256 (64-byte key). The main DB file, rollback journal, WAL frames, and temp files are all encrypted; the WAL `-shm` index stays plaintext (it's process-local coordination state, not row data).

```go
import "gosqlite.org/vfs/crypto"

key := make([]byte, 32) // derive from passphrase / keyring / HSM
name, fs, _ := crypto.New(crypto.Options{Key: key})
defer fs.Close()

db, _ := sql.Open("sqlite", "file:secret.db?vfs="+name)
```

Or in one call via `crypto.Open`, which registers the VFS, routes a typed [`Config`](configuration.md) through it, and bundles VFS teardown into `db.Close()`:

```go
db, _ := crypto.Open(
	sqlite.Config{Path: "secret.db", Pragmas: sqlite.RecommendedPragmas()},
	crypto.Options{Key: key}, // 32-byte Adiantum key (default cipher)
)
defer db.Close()
```

## Multiple recipients

To let several people open one encrypted database, each with their own key and no shared secret, pass `Options.Recipients` instead of `Options.Key`. A random data key encrypts the pages and is wrapped per recipient — an SSH key, a passphrase, or an age recipient built with [`crypto/keyring`](https://pkg.go.dev/gosqlite.org/crypto/keyring) — into a detached `"<path>.keyslot"` sidecar next to the database. Reopen with `Options.Identities` (none matching → `crypto.ErrNoIdentity`). `Options.Masters` + `Options.SignWith` pin ed25519 administrators: the keyslot is signed and a reader pinning the masters it trusts rejects one that isn't (`crypto.ErrUnauthorized`).

```go
alice, _ := keyring.SSHRecipient(alicePubKey)
db, _ := crypto.Open(cfg, crypto.Options{Recipients: []keyring.Recipient{alice, bob}})
// reopen: crypto.Open(cfg, crypto.Options{Identities: []keyring.Identity{aliceID}})
```

Change the recipient/master set on a closed database with `crypto.Rewrap(path, by, masters, members)` — it re-seals the sidecar in place (O(1), no re-encryption; only a current master may change a master-protected set, else `crypto.ErrNotMaster`). There is no `Rekey`: true cryptographic revocation needs a fresh key and a whole re-encryption, but re-encrypting the database file and rewriting the *detached* sidecar are two files with no atomic update — a crash between them would be unrecoverable. For true revocation, re-create the file or use the [compression VFS](compression.md) (its keyslot lives inside the container).

The keyslot sidecar must travel with the database (back it up and move the two together); losing it makes the database unrecoverable — the detached-header trade. Read-only recipients (writer-signed authenticated mode) are a [compression VFS](compression.md) feature, not available here: this VFS is a transparent page cipher over a vanilla SQLite file with no place for per-slot integrity.

## What to know

- **Confidentiality only** — no SQLCipher on-disk format compatibility, no MAC. SQLCipher's per-page HMAC integrity is not what we ship; for active-tamper threats pair with disk-level integrity (LUKS dm-integrity, ZFS).
- **Overhead** on a write-heavy microbenchmark is in the tens of percent; the exact factor depends on cipher and platform (Adiantum is faster than AES-XTS on most ARM, often the reverse on AES-NI x86). Measure with `go test -bench=BenchmarkInsert ./vfs/crypto/`.

## Composing

Add `Options.Recorder = crypto.NewSlogRecorder(slog.Default())` (or any custom `crypto.Recorder`) for per-IO observability. Stack `vfs/cksm` underneath via `Options.WrapVFS` for checksum-then-encrypt protection (see [Checksums](checksums.md)).

`vfs/crypto` is its own module (`gosqlite.org/vfs/crypto`) so its cipher dependencies stay out of the root graph. Runnable: [`vfs/crypto/example/`](../../vfs/crypto/example/main.go) (`crypto.Open` + a slog recorder). For an encrypted database with an ORM, use [LiteORM](https://liteorm.org), built on this driver — the gorm dialector has no encryption path. Package docs: [`vfs/crypto/doc.go`](../../vfs/crypto/doc.go). On-disk format + threat model: `vfs/crypto/doc.go`; coverage: [`dev/coverage/vfs.md`](../../dev/coverage/vfs.md).
