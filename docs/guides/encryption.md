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

## Several parties, integrity, or compression? → the vault container

`vfs/crypto` is confidentiality-only with a single raw key — a headerless, length-preserving page cipher with no extra files. When you need several people to open one database each with their own key and no shared secret, crash-safe key rotation, tamper-evident or rollback-resistant storage, or compression alongside encryption, reach for the [`vfs/vault`](vault.md) container. It reuses this same page cipher but carries a wrapped-key keyslot inside the database file, and compression and encryption are independent options there. The [vault guide](vault.md) is the home for recipients (no shared secret), masters and signing writers (so a non-writer recipient is read-only), authenticated mode (symmetric or ed25519-signed), the external rollback anchor, membership enumeration (`vault.Members`), and encrypted backups (`vault.Snapshot`).

## What to know

- **Confidentiality only** — no SQLCipher on-disk format compatibility, no MAC. SQLCipher's per-page HMAC integrity is not what we ship; for active-tamper threats pair with disk-level integrity (LUKS dm-integrity, ZFS).
- **Overhead** on a write-heavy microbenchmark is in the tens of percent; the exact factor depends on cipher and platform (Adiantum is faster than AES-XTS on most ARM, often the reverse on AES-NI x86). Measure with `go test -bench=BenchmarkInsert ./vfs/crypto/`.

## Composing

Add `Options.Recorder = crypto.NewSlogRecorder(slog.Default())` (or any custom `crypto.Recorder`) for per-IO observability. Stack `vfs/cksm` underneath via `Options.WrapVFS` for checksum-then-encrypt protection (see [Checksums](checksums.md)).

Anything built on a `*sqlite.DB` inherits the encryption transparently — including [`blobstore`](blobstore.md): open the store's database through an encrypting VFS and every object, chunk, and block it writes is encrypted on disk. Single-key with `vfs/crypto` (runnable: [`examples/encrypted-blobstore`](../../examples/encrypted-blobstore/main.go)); to several recipients, with tamper-evidence, through the [`vfs/vault`](vault.md) container (runnable: [`examples/vault-blobstore`](../../examples/vault-blobstore/main.go)).

`vfs/crypto` is its own module (`gosqlite.org/vfs/crypto`) so its cipher dependencies stay out of the root graph. Runnable: [`vfs/crypto/example/`](../../vfs/crypto/example/main.go) (`crypto.Open` + a slog recorder). For an encrypted database with an ORM, use [LiteORM](https://liteorm.org), built on this driver — the gorm dialector has no encryption path. Package docs: [`vfs/crypto/doc.go`](../../vfs/crypto/doc.go). On-disk format + threat model: `vfs/crypto/doc.go`; coverage: [`dev/coverage/vfs.md`](../../dev/coverage/vfs.md).
