// Package memdb implements a plain in-memory SQLite VFS — pages live
// in a Go-side map per database name, reads and writes hit it
// directly, and there is no journal, no WAL, and no on-disk
// representation.
//
// memdb is the smaller-surface cousin of
// [github.com/go-again/sqlite/vfs/mvcc]:
//
//   - **memdb** stores one canonical copy of each page; concurrent
//     readers see writes the instant the writer's xWrite returns.
//     No snapshot isolation, no per-transaction copy.
//   - **mvcc** maintains snapshots taken at SHARED-lock acquire and
//     buffers writes until commit, giving snapshot-isolated reads
//     across concurrent transactions at the cost of a per-commit
//     map copy.
//
// Use memdb for tests, scratch databases, and the build-the-DB-in-RAM
// pattern; use mvcc when you actually need snapshot isolation across
// concurrent readers and writers in the same process.
//
// # Naming convention
//
// Database file names follow the same shared/private split as mvcc:
//
//   - Names beginning with `/` (or the URI form `file:/name`) are
//     **shared** — every `sql.Open` of the same name within a single
//     [FS] sees the same page store, so two handles can talk to each
//     other.
//   - Names without a leading slash are **private** — each `sql.Open`
//     gets its own independent storage. Useful for table-test
//     isolation when many sub-tests share one registered FS.
//
//	name, fs, _ := memdb.New(memdb.Options{})
//	defer fs.Close()
//	db, _ := sql.Open("sqlite", "file:/shared.db?vfs="+name)
//
// # When NOT to use memdb
//
//   - You only need a single-conn in-memory DB → use SQLite's native
//     `:memory:` instead, no extra VFS needed.
//   - You need durability across process restarts → use a file-backed
//     VFS (the default).
//   - You need snapshot isolation between concurrent readers and a
//     mid-flight writer → use [vfs/mvcc] instead.
package memdb
