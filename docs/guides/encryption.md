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

## Multiple recipients and authentication

`vfs/crypto` is confidentiality-only with a single raw key — a headerless, length-preserving page cipher with no extra files. When you need several people to open one database with their own key and no shared secret (a wrapped data key), crash-safe key rotation, or tamper-evident storage (per-page integrity and a signed root), reach for [`vfs/vault`](https://pkg.go.dev/gosqlite.org/vfs/vault) — a self-contained container that carries the keyslot inside the database file, where compression and encryption are independent options (encrypt, compress, or both).

### Several recipients, no shared secret

Set `Options.Recipients` instead of `Options.Key`: a random data key encrypts the container and is wrapped once per recipient — an SSH key, a passphrase, or a generated X25519 pair (the age model). Any one recipient opens the database with their own identity.

```go
import (
	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
	"gosqlite.org/vfs/vault"
)

alice, aliceID, _ := keyring.GenerateX25519() // or keyring.SSHRecipient / keyring.PassphraseRecipient
bob, bobID, _ := keyring.GenerateX25519()

// Create: the data key is wrapped for both recipients into a keyslot inside the file.
db, _ := vault.Open(
	sqlite.Config{Path: "shared.db", Pragmas: sqlite.RecommendedPragmas()},
	vault.Options{Recipients: []keyring.Recipient{alice, bob}},
)
// ... use db exactly like *sql.DB ...
db.Close()

// Open: either recipient unlocks it with their own identity — no shared secret.
db, _ = vault.Open(sqlite.Config{Path: "shared.db"}, vault.Options{Identities: []keyring.Identity{aliceID}})
defer db.Close()
```

Change the membership on a **closed** database without re-encrypting via `vault.Rewrap` (re-wrap the data key to a new recipient set — O(1)); for true cryptographic revocation use `vault.Rekey` (re-encrypt under a fresh data key — O(database size), so a removed party and any rolled-back old keyslot read nothing). Pin administrators with `Options.Masters` + `Options.SignWith` to require a signed membership (only a master may `Rewrap`/`Rekey`; a reader pinning `Options.Masters` rejects a membership not signed by a trusted master).

### Tamper-evidence

By default encryption is confidentiality only. For integrity — a modified, truncated, or partially-rolled-back container fails to open — turn on authenticated mode, in one of two flavours:

- **`Options.Authenticate`** (symmetric) — the root is an HMAC keyed by a key derived from the data key, so any key holder writes and verifies. It protects against an attacker **without** the key and needs no extra keys (just `Key` or `Recipients`); modification or a partial rollback is rejected with `vault.ErrTampered`.
- **`Options.Writers`** (ed25519, requires `Masters`) — every commit is signed by a writer, so a recipient holding the read key who is **not** a writer can read and verify but cannot forge a write others accept: it is read-only (`Options.WriteAs` to write; otherwise the VFS refuses writes with `vault.ErrReadOnlyRecipient`).

```go
db, _ := vault.Open(cfg, vault.Options{Recipients: recipients, Authenticate: true}) // multi-recipient + tamper-evident
```

Authenticated mode is tamper-evident; full-rollback resistance is **opt-in**. The signed root binds the commit generation, so a state cannot be renumbered, but an attacker who can overwrite the file with a *complete, self-consistent earlier committed image* produces a still-validly-signed container that opens without error. Supply `Options.Anchor` — a monotonic counter kept OUTSIDE the file (a TPM/keystore counter, or `vault.FileAnchor` on separate storage) — to close that: each commit records its generation, and open rejects a generation below the recorded floor with `vault.ErrRolledBack`.

```go
anchor := vault.FileAnchor("/secure-mount/app.floor") // or a TPM/keystore-backed ReplayAnchor
db, _ := vault.Open(cfg, vault.Options{Key: key, Authenticate: true, Anchor: anchor})
```

The anchor is only as strong as its storage — on the same disk as the database it stops nothing. Without an anchor, `Rekey` is the durable revocation path (a fresh data key, so a rolled-back snapshot can no longer be read). To reclaim space after heavy churn, `vault.Compact` rewrites a closed database densely and returns freed blocks to the OS, continuing the generation so the anchor stays valid.

Compression composes orthogonally — add `Options.Level` to any of the above to compress then encrypt. Runnable: [`vfs/vault/example`](../../vfs/vault/example/main.go) walks the whole matrix (plain → compressed → encrypted → multi-recipient → authenticated → writer-signed → snapshot); [`examples/vault-blobstore`](../../examples/vault-blobstore/main.go) runs a blobstore over a multi-recipient, compressed, authenticated container.

## What to know

- **Confidentiality only** — no SQLCipher on-disk format compatibility, no MAC. SQLCipher's per-page HMAC integrity is not what we ship; for active-tamper threats pair with disk-level integrity (LUKS dm-integrity, ZFS).
- **Overhead** on a write-heavy microbenchmark is in the tens of percent; the exact factor depends on cipher and platform (Adiantum is faster than AES-XTS on most ARM, often the reverse on AES-NI x86). Measure with `go test -bench=BenchmarkInsert ./vfs/crypto/`.

## Composing

Add `Options.Recorder = crypto.NewSlogRecorder(slog.Default())` (or any custom `crypto.Recorder`) for per-IO observability. Stack `vfs/cksm` underneath via `Options.WrapVFS` for checksum-then-encrypt protection (see [Checksums](checksums.md)).

Anything built on a `*sqlite.DB` inherits the encryption transparently — including [`blobstore`](blobstore.md): open the store's database through an encrypting VFS and every object, chunk, and block it writes is encrypted on disk. Single-key with `vfs/crypto` (runnable: [`examples/encrypted-blobstore`](../../examples/encrypted-blobstore/main.go)); to several recipients, with tamper-evidence, through `vfs/vault` (runnable: [`examples/vault-blobstore`](../../examples/vault-blobstore/main.go)).

`vfs/crypto` is its own module (`gosqlite.org/vfs/crypto`) so its cipher dependencies stay out of the root graph. Runnable: [`vfs/crypto/example/`](../../vfs/crypto/example/main.go) (`crypto.Open` + a slog recorder). For an encrypted database with an ORM, use [LiteORM](https://liteorm.org), built on this driver — the gorm dialector has no encryption path. Package docs: [`vfs/crypto/doc.go`](../../vfs/crypto/doc.go). On-disk format + threat model: `vfs/crypto/doc.go`; coverage: [`dev/coverage/vfs.md`](../../dev/coverage/vfs.md).
