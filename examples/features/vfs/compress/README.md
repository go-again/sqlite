# Compressed database — moved to the `vfs/compress` module

`vfs/compress` is a **separate module** (`gosqlite.org/vfs/compress`), so its runnable example lives inside that module — that keeps its codec dependency out of the core dependency graph for everyone who never opens a compressed database.

Run it from the module directory:

```sh
cd vfs/compress && go run ./example
```

It opens a compressed database with `compress.Open`, writes many compressible rows, closes (which compresses the working copy to disk), prints the on-disk size and ratio, then reopens and queries it in place.

- Source: [`vfs/compress/example/main.go`](../../../../vfs/compress/example/main.go)
- API reference: [pkg.go.dev/gosqlite.org/vfs/compress](https://pkg.go.dev/gosqlite.org/vfs/compress)
