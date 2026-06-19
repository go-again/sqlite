---
title: In-memory & embedded databases
description: MVCC and direct in-memory VFSes, embed.FS-backed read-only databases, and io.ReaderAt buffers.
sidebar:
  order: 10
---

# In-memory & embedded databases

Several VFSes back a database with memory or a read-only buffer instead of the OS filesystem. Pick by the isolation contract you want.

## In-memory: MVCC vs direct

```go
// Snapshot-isolation reads + atomic-publish writes.
import "github.com/go-again/sqlite/vfs/mvcc"
name, fs, _ := mvcc.New(mvcc.Options{})
defer fs.Close()
db, _ := sql.Open("sqlite", "file:/shared.db?vfs="+name)   // SHARED
db2, _ := sql.Open("sqlite", "file:scratch.db?vfs="+name)  // PRIVATE
```

```go
// Direct per-page store, no MVCC — writes visible to readers instantly.
import "github.com/go-again/sqlite/vfs/memdb"
name, fs, _ := memdb.New(memdb.Options{})
defer fs.Close()
db, _ := sql.Open("sqlite", "file:/cache.db?vfs="+name)
```

`vfs/mvcc` gives reader snapshots at lock-acquire time and republishes the snapshot atomically on commit; pair with the standard busy-timeout retry for concurrent writers. `vfs/memdb` is the smaller-surface alternative — no snapshot copy on commit; concurrent writers see each other immediately. Both use the leading-`/` convention (`file:/name`) for shared stores vs no-leading-slash (`file:name`) for per-handle private stores. Runnable: [`examples/features/vfs/mvcc/`](../../examples/features/vfs/mvcc/main.go), [`examples/features/vfs/memdb/`](../../examples/features/vfs/memdb/main.go).

For simple shared in-memory tests without a VFS, `sqlite.OpenShared(name)` is the one-liner ([Configuration](configuration.md)).

## embed.FS-backed read-only databases

```go
import "github.com/go-again/sqlite/vfs"

//go:embed seed.db
var seed embed.FS

name, _, _ := vfs.New(seed)
db, _ := sql.Open("sqlite3", "file:seed.db?vfs="+name+"&mode=ro")
```

`vfs.New(fs.FS)` exposes any read-only `io/fs.FS` (embed.FS, fstest.MapFS, zip-fs) as a VFS. Runnable: [`examples/features/vfs/embed/`](../../examples/features/vfs/embed/main.go).

## io.ReaderAt-backed databases

When you already have a `[]byte` or `io.ReaderAt`, use the lighter `vfs.NewReader`:

```go
bs, _ := os.ReadFile("seed.db")
name, fs, _ := vfs.NewReader(bytes.NewReader(bs), int64(len(bs)))
defer fs.Close()

db, _ := sql.Open("sqlite", "file:db?vfs="+name+"&mode=ro")
```

The file name inside the VFS is always `db`. Useful for shipping a sealed read-only DB as a Go variable, or mmap-backed buffers that don't want to round-trip through `fs.FS`.

To back a **writable** database with your own Go storage, see [Custom VFS](custom-vfs.md).
