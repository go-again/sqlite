// Package vfs lets you back a SQLite database with storage you control,
// in pure Go. It offers two surfaces, smallest first:
//
//   - [New] / [NewReader] — expose any read-only io/fs.FS (or io.ReaderAt)
//     as a SQLite VFS. The zero-code path for shipping seed data inside a
//     binary, reading a zip-backed catalog, or serving a database from a
//     memory buffer — without ever touching the real disk.
//   - [Register] with a user-implemented [VFS] / [File] — a full,
//     writable virtual file system in pure Go: an object-store backend,
//     a fault-injecting wrapper, a tmpfs-on-a-budget. This is the
//     building block downstream code uses to put SQLite on storage the
//     driver never anticipated, without forking the driver or touching
//     the transpiled lib/.
//
// Pair either with the parent gosqlite.org driver and
// select it from a DSN via ?vfs=<name>.
//
// # Read-only fs.FS exposure
//
// The canonical use case is shipping seed data inside the binary. A CLI
// tool that wants to read from a pre-populated SQLite catalog embeds the
// database file, registers it as a VFS via New, and opens it as a normal
// *sql.DB with `?vfs=<name>&mode=ro`. The three entry points:
//
//   - New(fs.FS) (name, *FS, error) — registers fs under a fresh unique
//     name; pass the name back via the DSN's ?vfs= parameter.
//   - NewReader(io.ReaderAt, size int64) (name, *FS, error) — same shape
//     but wraps any io.ReaderAt + known size as the single underlying
//     file (named "db"); no fs.FS needed.
//   - FS — type alias for modernc.org/sqlite/vfs.FS, a handle on the
//     registered VFS for lifetime control.
//
// See examples/features/vfs/embed/ for the runnable embed.FS demonstration.
//
// This surface is a thin re-export of modernc.org/sqlite/vfs; the C side
// is transpiled per-target there, so platform coverage matches its
// matrix. The VFS is read-only — any write the engine attempts is
// rejected; append &mode=ro so SQLite refuses writes at open time and
// you get a clear error rather than a surprise mid-query.
//
// # User-implementable VFS
//
// Implement [VFS] (Open/Delete/Access/FullPathname) and [File]
// (ReadAt/WriteAt/Truncate/Sync/Size/locking/Close), then:
//
//	err := vfs.Register("myvfs", myImpl)            // once at startup
//	db, _ := sql.Open("sqlite", "file:app.db?vfs=myvfs")
//	// ... use db ...
//	db.Close()
//	vfs.Unregister("myvfs")                          // after every db is closed
//
// Embed [NoLock] in a File to satisfy the advisory-lock trio with
// accept-everything semantics — correct for any single-process backend.
// Return a [VFSError] from any method to surface a specific SQLITE_*
// result code (e.g. SQLITE_READONLY, SQLITE_BUSY); a plain error becomes
// SQLITE_IOERR. The generic dispatcher copies every buffer at the C
// boundary, delegates wall-clock/sleep/randomness to the platform VFS,
// and recovers the implementation through the same token-registry
// machinery (internal/cabi) that backs the built-in vfs/memdb and
// vfs/mvcc backends — so a hand-written VFS gets the same vetted
// trampolines, not a second copy.
//
// A custom VFS runs in rollback-journal mode by default. To unlock WAL,
// have your File also implement [ShmFile] — a single method declaring a
// sharing group; the dispatcher owns the WAL shared memory and lock
// table, so you never touch unsafe memory or the lock protocol. WAL
// coordination is in-process (it backs multiple database/sql
// connections to one Go-managed database within a process, not
// cross-process WAL over a real disk).
//
// [Unregister] refuses to remove a VFS while any database is still open
// against it — close every handle first.
//
// # Instrumenting a VFS
//
// [Wrap] decorates any VFS (yours, or a built-in backend) so every
// Open/Read/Write/Sync reports its latency, byte count, and error to a
// [Recorder] — the building block for I/O dashboards. [NewSlogRecorder]
// is a ready-made Recorder that logs each op via log/slog. A nil
// Recorder returns the base unchanged, so tracing toggles cleanly:
//
//	impl := myVFS()
//	if *trace { impl = vfs.Wrap(impl, vfs.NewSlogRecorder(logger)) }
//	vfs.Register("app", impl)
//
// Wrap observes only; a backend that injects faults or shapes latency
// implements [VFS] / [File] directly.
//
// # Sub-packages
//
// Built on the same machinery, ready to use:
//
//   - vfs/memdb — plain in-memory VFS (per-page store under one RWMutex).
//   - vfs/mvcc — in-memory VFS with snapshot-isolation reads.
//   - vfs/crypto — encryption-at-rest, layered over a base VFS.
//   - vfs/cksm — page-checksum corruption detection, layered over a base VFS.
//
// # See also
//
//   - examples/features/vfs/custom/ — a writable in-memory VFS on the public interface.
//   - examples/features/vfs/embed/ — minimal read-only embed.FS demonstration.
//   - gorm/examples/vfs/ — the read-only flow with a gorm dialector on top.
package vfs
