# Coverage — vfs/ wrappers

Tracks each sub-package under [`vfs/`](../vfs/). The root `vfs/` package
itself re-exports `modernc.org/sqlite/vfs` so any `fs.FS` becomes a
read-only SQLite VFS; everything else wraps a base VFS to add a
specific capability (encryption, checksum) by intercepting xRead /
xWrite.

Status legend:

- ✓ landed — code + tests + docs shipped
- ⚠ partial — code shipped but coverage incomplete, or a feature gated
- ✗ deferred — analyzed and chosen for a later round
- ✗ skipped — analyzed and intentionally dropped (overlap with existing surface)

## Sub-packages

| sub-package | Upstream | Status | Entry | Test pin |
|---|---|---|---|---|
| `vfs/` (root, fs.FS adapter) | [ncruces vfs/readervfs](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/readervfs) | ✓ landed | `vfs.New(fs.FS)` | `vfs/vfs_test.go` |
| `vfs/crypto` | [ncruces vfs/adiantum](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/adiantum) + [vfs/xts](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/xts) | ✓ landed | `crypto.New(Options)` | `vfs/crypto/integration_test.go` |
| `vfs/cksm` | [ncruces vfs/cksm.go](https://github.com/ncruces/go-sqlite3/blob/main/vfs/cksm.go) | ✓ landed | `cksm.New(Options)` + `(*sqlite.Conn).EnableChecksums(schema)` | `vfs/cksm/cksm_test.go` |
| `vfs/mvcc` | [ncruces vfs/mvcc](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/mvcc) | ✓ landed (Go-native re-implementation, no wbt dep) | `mvcc.New(Options)` | `vfs/mvcc/mvcc_test.go` |
| `vfs/memdb` | [ncruces vfs/memdb](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/memdb) | ✓ landed | `memdb.New(Options)` | `vfs/memdb/memdb_test.go` |
| `vfs.NewReader(io.ReaderAt, size)` | [ncruces vfs/readervfs](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/readervfs) | ✓ landed | `vfs.NewReader(r, n)` | `vfs/vfs_test.go` |

`vfs/crypto` covers both ncruces sub-packages: pick between Adiantum
(default, 32-byte key, length-preserving wide-block cipher) and
AES-XTS-256 (64-byte key, mandated by some compliance regimes) via the
`Options.Cipher` field. The cipher's tweak is domain-separated by file
kind (main DB / journal / WAL / temp DB / temp / sub-journal) so the
same plaintext at the same page number in different file kinds
produces distinct ciphertext. Encryption-at-rest covers everything that
hits xRead/xWrite; the WAL `-shm` index file is plaintext because it is
process-local coordination state, not row data.

### Chaining

`vfs/crypto` and `vfs/cksm` both accept an `Options.WrapVFS string`
field that names another registered VFS to layer on top of (rather
than the system default). The canonical stack is checksum-on-the-
inside / encrypt-on-the-outside:

```go
cksmName, cksmFS, _ := cksm.New(cksm.Options{})
defer cksmFS.Close()

cryptoName, cryptoFS, _ := crypto.New(crypto.Options{
    Key:     key,
    WrapVFS: cksmName,                            // layer cksm beneath crypto
})
defer cryptoFS.Close()

sql.Open("sqlite", "file:db.db?vfs="+cryptoName)
```

On write, crypto encrypts and forwards to cksm, which stamps the
checksum trailer over the ciphertext and forwards to the system
default. On read, the order reverses: cksm verifies first (proving
the ciphertext arrived intact), then crypto decrypts. Per-file state
in each layer lives at the wrapped VFS's `szOsFile` offset, so the
layers don't collide.

`vfs/cksm` is a page-level checksum VFS — every main-DB page write
computes a 32-bit Fletcher-style rolling sum over the page body and
stamps the 8-byte trailer; every read verifies it. Activates only when
the database's `reserved_bytes` byte (offset 20 of the SQLite header)
reads 8, which `(*sqlite.Conn).EnableChecksums(schema)` sets via
`SQLITE_FCNTL_RESERVE_BYTES` and then `VACUUM`s the database to rewrite
every existing page with the trailer in place.

## Skipped / out of scope

| feature | Reason |
|---|---|
| ncruces `vfs/` core framework (api.go, file.go, lock_*.go, os_*.go, shm_*.go) | Specific to ncruces' Wazero-WASM runtime. We use `modernc.org/sqlite/vfs`, whose core framework is the transpiled SQLite default VFS — different runtime, same end behaviour. |

## Adding a new VFS wrapper

1. Pick an upstream pattern from the [ncruces/go-sqlite3 vfs/ tree](https://github.com/ncruces/go-sqlite3/tree/main/vfs)
   or the SQLite extensions registry.
2. Mirror the `vfs/crypto` layout: `cksm.go`-style `New`/`Close`/`FS`,
   `vfs.go`-style `perFileState` + `xOpen` trampoline,
   `iomethods.go`-style 12 io-method trampolines plus `callXFoo`
   wrappers, optional `derive_*.go` or `cipher.go` for any helpers.
3. Use `internal/cabi.FuncPointer` for Go→C function-pointer slots and
   `internal/cabi.AsFunc[F]` for C→Go reads back from stored uintptrs,
   or the typed `cabi.CallX*` family for io-method slot dispatch. (All
   three are the same pattern as `vfs/crypto`.)
4. Add a row to the table above; flip to ✓ landed once tests + lint
   pass.
5. Add a one-line entry to [`llms.txt`](../llms.txt) under "Per-package
   overviews" so consumer agents find it.
6. Optional: drop a runnable example under `examples/vfs-<name>/`.

Last reviewed against ncruces/go-sqlite3 main on 2026-05-29.
