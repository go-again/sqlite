# Vault blobstore (multiple recipients, tamper-evident)

A [`blobstore`](../../docs/guides/blobstore.md) is just SQL and incremental BLOB I/O over a `*sqlite.DB`, so whatever VFS protects that database protects the store — no blobstore-specific configuration. This example opens the store's database through [`vfs/vault`](../../docs/guides/vault.md), the container where compression and encryption are independent options.

It encrypts the container **to two recipients** (Alice and Bob, each opens with their own key — no shared secret), with compression and **authenticated** (tamper-evident) mode turned on. It writes an object, confirms the plaintext is absent from the raw file, reads it back as each recipient, and confirms a stranger (not a recipient) is refused. (For full rollback resistance, add `Options.Anchor`; this example does not.)

For the single-key case, see the sibling [`encrypted-blobstore`](../encrypted-blobstore/) (over `vfs/crypto`).

It is a separate module (it composes `gosqlite.org/blobstore`, `gosqlite.org/vfs/vault`, and `gosqlite.org/crypto/keyring` via local `replace` directives, so no published module gains the others in its graph).

```
cd examples/vault-blobstore
go run .     # or: just example vault-blobstore
go test ./   # pins the composition
```
