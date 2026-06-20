---
title: Custom VFS
description: Back a writable database with arbitrary Go storage — implement vfs.VFS / vfs.File, optional WAL via ShmFile, and Wrap instrumentation.
sidebar:
  order: 11
---

# Custom VFS

Implement a SQLite virtual file system in pure Go — an object-store backend, a fault injector, a tmpfs-on-a-budget — and register it under a name, without forking the driver. One generic dispatcher drives it through the same vetted C-ABI machinery the built-in VFSes use.

```go
import "gosqlite.org/vfs"

err := vfs.Register("myvfs", myImpl) // once at startup
db, _ := sql.Open("sqlite", "file:app.db?vfs=myvfs")
// ... use db ...
db.Close()
vfs.Unregister("myvfs")              // after every db against it is closed
```

Implement [`vfs.VFS`](https://pkg.go.dev/gosqlite.org/vfs#VFS) (`Open`/`Delete`/`Access`/`FullPathname`) and [`vfs.File`](https://pkg.go.dev/gosqlite.org/vfs#File) (`ReadAt`/`WriteAt`/`Truncate`/`Sync`/`Size`/locking/`Close`). Embed `vfs.NoLock` to satisfy the advisory-lock trio with accept-everything semantics — correct only for **single-connection** access (multiple connections in WAL mode need real locking; see [WAL](#wal--the-shmfile-capability)). Return a `vfs.VFSError` from any method to surface a specific `SQLITE_*` result code; a plain error becomes `SQLITE_IOERR`.

A complete ~80-line in-memory backend is at [`examples/features/vfs/custom/`](../../examples/features/vfs/custom/main.go).

## WAL — the ShmFile capability

A custom VFS runs in rollback-journal mode by default. To unlock WAL, have your `File` also implement `vfs.ShmFile` — a single `ShmGroup() string` method declaring which open files share a WAL index. The dispatcher owns the shared memory and the 8-slot WAL lock table, so you never touch unsafe memory or the shared-memory lock protocol. WAL coordination is in-process (it backs multiple `database/sql` connections to one Go-managed database within a process, not cross-process WAL over a real disk).

> **Multi-connection WAL needs real db-file locking — do not embed `vfs.NoLock`.** The dispatcher arbitrates the WAL *shared-memory* locks, but SQLite still gates destructive operations — notably the checkpoint it runs when a connection closes, which resets the `-wal` — on first acquiring an EXCLUSIVE *db-file* lock. `vfs.NoLock` grants that EXCLUSIVE even while other connections are active, so the close-checkpoint can reset the WAL under a concurrent writer and corrupt the database. Implement real `Lock` / `Unlock` / `CheckReservedLock` on the main db file: many connections may share `LockShared`; `LockExclusive` must fail while any other connection holds `LockShared`. The reference `File` in the `vfs` package tests is a complete in-process example.

## Instrumentation — Wrap

`vfs.Wrap(base, recorder)` decorates any VFS (yours or a built-in) so every Open/Read/Write/Sync reports its latency, byte count, and error to a `vfs.Recorder`. `vfs.NewSlogRecorder` logs each op via `log/slog`; a nil recorder returns the base unchanged, so tracing toggles cleanly. The `vfs-custom` example prints I/O stats this way.

`vfs.Unregister` refuses to remove a VFS while any database is still open against it — close every handle first. Coverage + the on-interface contract: [`dev/coverage/vfs.md`](../../dev/coverage/vfs.md) and `vfs/doc.go`.
