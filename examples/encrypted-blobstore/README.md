# Encrypted blobstore

A [`blobstore`](../../docs/guides/blobstore.md) inherits encryption at rest for free: open the store's database through [`vfs/crypto`](../../docs/guides/encryption.md) and every object, chunk, and block it writes is encrypted on disk, with no blobstore-specific configuration — the store is just SQL and incremental BLOB I/O over a `*sqlite.DB`, so whatever VFS encrypts that database encrypts the store.

This example encrypts the database to two recipients (an age-style keyslot), writes an object, confirms the plaintext is absent from the raw file, then reopens as one recipient and reads it back — the `vfs/crypto` multi-recipient model applied to a blob store.

It is a separate module (it composes `gosqlite.org/blobstore`, `gosqlite.org/vfs/crypto`, and `gosqlite.org/crypto/keyring` via local `replace` directives, so neither published module gains the other in its graph).

```
cd examples/encrypted-blobstore
go run .     # or: just example encrypted-blobstore
go test ./   # pins the composition
```
