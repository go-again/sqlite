---
name: custom-vfs
description: Use when backing a writable SQLite database with arbitrary Go storage (object store, fault injector, custom in-memory) in a Go app with gosqlite — implementing the vfs.VFS / vfs.File interfaces and vfs.Register.
---

# Implementing a custom VFS

Implement `vfs.VFS` + `vfs.File`, register under a name, reference via `?vfs=`. The dispatcher owns the C-ABI machinery.

```go
import "gosqlite.org/vfs"

vfs.Register("myvfs", myImpl)              // once at startup
db, _ := sql.Open("sqlite", "file:app.db?vfs=myvfs")
// ... use db ...
db.Close()
vfs.Unregister("myvfs")                    // only after every db against it is closed
```

## Interfaces

```go
type VFS interface {
	Open(name string, flags OpenFlags) (File, OpenFlags, error)
	Delete(name string, syncDir bool) error
	Access(name string, op AccessOp) (bool, error)
	FullPathname(name string) (string, error)
}
type File interface {
	io.ReaderAt; io.WriterAt
	Truncate(int64) error; Sync(SyncFlags) error; Size() (int64, error)
	Lock(LockLevel) error; Unlock(LockLevel) error; CheckReservedLock() (bool, error)
	SectorSize() int; DeviceCharacteristics() DeviceFlags; Close() error
}
```

- **Embed `vfs.NoLock`** in your File to get the lock trio for free — correct only for **single-connection** access (see the WAL caveat below; multi-connection WAL needs real locking).
- **Errors:** return `&vfs.VFSError{Code: sqlite3.SQLITE_READONLY}` (or any `SQLITE_*`) for a specific code; a plain error becomes `SQLITE_IOERR`. A short read past EOF returns `io.EOF` (dispatcher zero-fills + reports SHORT_READ).
- **Buffers are copied at the boundary** — a `File.ReadAt` is handed a fresh slice; don't alias it past the call.

## WAL

Default is rollback-journal. For WAL, also implement `vfs.ShmFile` (one method, `ShmGroup() string`, declaring which files share a WAL index). The dispatcher owns the shared memory + WAL lock table. In-process only.

> **Multi-connection WAL needs real db-file locking — do NOT embed `vfs.NoLock`.** The dispatcher arbitrates the WAL *shared-memory* locks, but SQLite still gates destructive operations — notably the checkpoint it runs when a connection closes, which resets the `-wal` — on first acquiring an EXCLUSIVE *db-file* lock. `NoLock` hands out that EXCLUSIVE even while other connections are active, so the close-checkpoint can reset the WAL under a concurrent writer and corrupt the database. Implement real `Lock` / `Unlock` / `CheckReservedLock` on the main db file: many holders may share `LockShared`; `LockExclusive` must fail while any other connection holds `LockShared`. The reference `File` in the `vfs` package tests shows the full pattern.

## Instrumentation

`vfs.Wrap(base, vfs.NewSlogRecorder(logger))` logs per-op latency/bytes/errors over any VFS; nil recorder returns base unchanged.

Copy-paste starting point: `examples/features/vfs/custom/main.go`. Full reference: [`docs/guides/custom-vfs.md`](../../docs/guides/custom-vfs.md). For the built-in VFSes (encryption, in-memory, embed.FS), use the `encryption-and-vfs` skill instead.
