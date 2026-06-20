# Encryption at rest — moved to the `vfs/crypto` module

`vfs/crypto` is a **separate module** (`gosqlite.org/vfs/crypto`), so its runnable example lives inside that module — that keeps `lukechampine.com/adiantum` and `golang.org/x/crypto` out of the core dependency graph for everyone who never opens an encrypted database.

Run it from the module directory:

```sh
cd vfs/crypto && go run ./example
```

It opens an Adiantum-encrypted database with `crypto.Open`, writes a row, then reopens the raw file to prove the plaintext is not present on disk.

- Source: [`vfs/crypto/example/main.go`](../../../../vfs/crypto/example/main.go)
- Guide: [`docs/guides/encryption.md`](../../../../docs/guides/encryption.md)
- API reference: [pkg.go.dev/gosqlite.org/vfs/crypto](https://pkg.go.dev/gosqlite.org/vfs/crypto)
