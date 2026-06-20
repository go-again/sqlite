# Blob storage — moved to the `blobstore` module

`blobstore` is a **separate module** (`gosqlite.org/blobstore`), so its runnable example lives inside that module — that keeps its codec dependency out of the core dependency graph for everyone who never stores a blob.

Run it from the module directory:

```sh
cd blobstore && go run ./example
```

It stores a large, growable byte object as an `io.WriterAt` / `io.ReaderAt` (out-of-order writes, sparse holes, truncate, delete), then opens a second store with `WithCompression` to show the same API storing objects transparently compressed.

- Source: [`blobstore/example/main.go`](../../../../blobstore/example/main.go)
- Guide: [`docs/guides/blobstore.md`](../../../../docs/guides/blobstore.md)
- API reference: [pkg.go.dev/gosqlite.org/blobstore](https://pkg.go.dev/gosqlite.org/blobstore)
