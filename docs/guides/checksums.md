---
title: Corruption detection
description: Pure-Go page-checksum VFS — Fletcher-style trailer per page; surfaces silent bit-rot as SQLITE_IOERR_DATA.
sidebar:
  order: 9
---

# Corruption detection at rest

`vfs/cksm` is a pure-Go, page-level Fletcher-style checksum VFS — an 8-byte trailer per page, on-disk compatible with SQLite's `cksumvfs` extension.

```go
import (
	"gosqlite.org/vfs/cksm"
	sqlite "gosqlite.org"
)

name, fs, _ := cksm.New(cksm.Options{})
defer fs.Close()

db, _ := sql.Open("sqlite", "file:journal.db?vfs="+name)
sc, _ := db.Conn(ctx)
sc.Raw(func(d any) error { return d.(*sqlite.Conn).EnableChecksums("main") })
```

`(*sqlite.Conn).EnableChecksums("main")` sets `reserved_bytes=8` on the schema and `VACUUM`s so every existing page is rewritten with the trailer.

## What to know

- On reopen the VFS detects byte 20 == 8 in the SQLite header and auto-activates verification; a flipped bit surfaces as `SQLITE_IOERR_DATA` on read instead of silent corruption.
- On-disk compatible with stock SQLite + `cksumvfs` — a database written here is readable by that combination.

## Composing

Both `cksm.Options` and `crypto.Options` accept `WrapVFS` to stack — register cksm first, then point crypto at it for checksum-on-the-inside / encrypt-on-the-outside.

Runnable: [`examples/features/vfs/cksm/`](../../examples/features/vfs/cksm/main.go) (corrupt-a-byte demo). Trailer format: `vfs/cksm/doc.go`; coverage: [`dev/coverage/vfs.md`](../../dev/coverage/vfs.md).
