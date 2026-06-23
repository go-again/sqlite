# Encrypted blobstore

A [`blobstore`](../../docs/guides/blobstore.md) inherits encryption at rest for free: open the store's database through [`vfs/crypto`](../../docs/guides/encryption.md) and every object, chunk, and block it writes is encrypted on disk, with no blobstore-specific configuration — the store is just SQL and incremental BLOB I/O over a `*sqlite.DB`, so whatever VFS encrypts that database encrypts the store.

This example encrypts the database with a raw key, writes an object, confirms the plaintext is absent from the raw file, then reopens with the same key and reads it back. `vfs/crypto` is confidentiality-only; for multi-recipient or tamper-evident encryption under a store, the same composition works with `gosqlite.org/vfs/vault`.

It is a separate module (it composes `gosqlite.org/blobstore` and `gosqlite.org/vfs/crypto` via local `replace` directives, so neither published module gains the other in its graph).

```
cd examples/encrypted-blobstore
go run .     # or: just example encrypted-blobstore
go test ./   # pins the composition
```
