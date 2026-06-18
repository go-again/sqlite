# Coverage — vfs/ wrappers

Tracks each sub-package under [`vfs/`](../vfs/). The root `vfs/` package
offers two surfaces: it re-exports `modernc.org/sqlite/vfs` so any
`fs.FS` becomes a read-only SQLite VFS, and it exposes a public
user-implementable `VFS` / `File` interface (`vfs.Register`) so
downstream code can back a *writable* database with arbitrary Go storage
through one generic dispatcher. Everything else wraps a base VFS to add a
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
| `vfs/` (root, user-implementable VFS) | [ncruces vfs core](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs) | ✓ landed (rollback-journal + WAL via `ShmFile`; in-process shm) | `vfs.Register(name, VFS)` + `vfs.VFS` / `vfs.File` / `vfs.ShmFile` interfaces | `vfs/interface_test.go`, `vfs/shm_test.go` |
| `vfs/crypto` | [ncruces vfs/adiantum](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/adiantum) + [vfs/xts](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/xts) | ✓ landed | `crypto.New(Options)` | `vfs/crypto/integration_test.go` |
| `vfs/cksm` | [ncruces vfs/cksm.go](https://github.com/ncruces/go-sqlite3/blob/main/vfs/cksm.go) | ✓ landed | `cksm.New(Options)` + `(*sqlite.Conn).EnableChecksums(schema)` | `vfs/cksm/cksm_test.go` |
| `vfs/mvcc` | [ncruces vfs/mvcc](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/mvcc) | ✓ landed (Go-native re-implementation, no wbt dep) | `mvcc.New(Options)` | `vfs/mvcc/mvcc_test.go` |
| `vfs/memdb` | [ncruces vfs/memdb](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/memdb) | ✓ landed | `memdb.New(Options)` | `vfs/memdb/memdb_test.go` |
| `vfs.NewReader(io.ReaderAt, size)` | [ncruces vfs/readervfs](https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/readervfs) | ✓ landed | `vfs.NewReader(r, n)` | `TestVFS_OpenFromReaderAt`, `TestVFS_NewReader_NilRejected`, `TestVFS_NewReader_NegativeSizeRejected` (`vfs/vfs_test.go`) |

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

## User-implementable VFS interface

`vfs.Register(name, impl)` drives any implementation of the public
`vfs.VFS` (Open/Delete/Access/FullPathname) and `vfs.File`
(ReadAt/WriteAt/Truncate/Sync/Size/Lock/Unlock/CheckReservedLock/
SectorSize/DeviceCharacteristics/Close) interfaces through one shared
set of generic trampolines — the same `internal/cabi` registry +
function-pointer machinery the bespoke sub-packages use, lifted to
dispatch into a Go interface. Embed `vfs.NoLock` for accept-everything
advisory locking (the single-process case); return a `*vfs.VFSError` to
surface a specific SQLITE_* result code; implement the optional
`vfs.FileControl` capability interface to handle file-control opcodes.
The dispatcher copies every buffer at the C boundary, delegates
clock/sleep/randomness to the platform VFS, and refuses `Unregister`
while any database is still open.

`vfs.Wrap(base, recorder)` (Phase 3) decorates any VFS so each
Open/Read/Write/Sync reports latency + byte count + error to a
`vfs.Recorder`; `vfs.NewSlogRecorder` is the built-in log/slog Recorder
(Debug per op, Warn on a genuine fault, with the expected `io.EOF`
short-read carved out). A nil Recorder returns the base unchanged. The
wrapper forwards the optional `vfs.FileControl` capability so wrapping
never silently drops it. Test pin: `vfs/wrap_test.go`.

WAL is supported through the optional `vfs.ShmFile` capability: a File
that also implements `ShmGroup() string` advertises the `xShm*` methods,
so SQLite offers it WAL mode. The dispatcher owns the shared-memory
regions (stable C allocations the WAL index lives in) and the 8-slot WAL
lock table — user code only declares which open files share a WAL index
(same `ShmGroup` key → shared shm). Coordination is in-process: it backs
multiple `database/sql` connections to one Go-managed database within a
process, not cross-process WAL over a real filesystem (the platform VFS
remains the tool for that). A File that does not implement `ShmFile`
stays on the `iVersion 1` methods table and runs in rollback-journal
mode. Test pin: `vfs/shm_test.go` (WAL round-trip, shared-reader
visibility, and a 1-writer/4-reader concurrent stress under `-race`).

The reference `refMemVFS` in `vfs/interface_test.go` is a complete
writable in-memory VFS on this interface and doubles as a copy-paste
template; `examples/features/vfs/custom/` is the runnable version (with `vfs.Wrap`
instrumentation).

This is the from-scratch path; the wrap-and-forward sub-packages below
(crypto/cksm) layer over an existing VFS instead and keep their bespoke
trampolines.

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

Last reviewed against ncruces/go-sqlite3 main on 2026-06-13.
