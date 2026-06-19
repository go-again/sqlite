---
title: Custom VFS
description: Back a writable database with arbitrary Go storage — implement vfs.VFS / vfs.File, optional WAL via ShmFile, and Wrap instrumentation.
sidebar:
  order: 11
---

# Custom VFS

Implement a SQLite virtual file system in pure Go — an object-store backend, a fault injector, a tmpfs-on-a-budget — and register it under a name, without forking the driver. One generic dispatcher drives it through the same vetted C-ABI machinery the built-in VFSes use.

```go
import "github.com/go-again/sqlite/vfs"

err := vfs.Register("myvfs", myImpl) // once at startup
db, _ := sql.Open("sqlite", "file:app.db?vfs=myvfs")
// ... use db ...
db.Close()
vfs.Unregister("myvfs")              // after every db against it is closed
```

Implement [`vfs.VFS`](https://pkg.go.dev/github.com/go-again/sqlite/vfs#VFS) (`Open`/`Delete`/`Access`/`FullPathname`) and [`vfs.File`](https://pkg.go.dev/github.com/go-again/sqlite/vfs#File) (`ReadAt`/`WriteAt`/`Truncate`/`Sync`/`Size`/locking/`Close`). Embed `vfs.NoLock` to satisfy the advisory-lock trio with accept-everything semantics (correct for single-process backends). Return a `vfs.VFSError` from any method to surface a specific `SQLITE_*` result code; a plain error becomes `SQLITE_IOERR`.

A complete ~80-line in-memory backend is at [`examples/features/vfs/custom/`](../../examples/features/vfs/custom/main.go).

## WAL — the ShmFile capability

A custom VFS runs in rollback-journal mode by default. To unlock WAL, have your `File` also implement `vfs.ShmFile` — a single `ShmGroup() string` method declaring which open files share a WAL index. The dispatcher owns the shared memory and the 8-slot WAL lock table, so you never touch unsafe memory or the lock protocol. WAL coordination is in-process (it backs multiple `database/sql` connections to one Go-managed database within a process, not cross-process WAL over a real disk).

## Instrumentation — Wrap

`vfs.Wrap(base, recorder)` decorates any VFS (yours or a built-in) so every Open/Read/Write/Sync reports its latency, byte count, and error to a `vfs.Recorder`. `vfs.NewSlogRecorder` logs each op via `log/slog`; a nil recorder returns the base unchanged, so tracing toggles cleanly. The `vfs-custom` example prints I/O stats this way.

`vfs.Unregister` refuses to remove a VFS while any database is still open against it — close every handle first. Coverage + the on-interface contract: [`dev/coverage/vfs.md`](../../dev/coverage/vfs.md) and `vfs/doc.go`.
