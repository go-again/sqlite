// Package mvcc implements an in-memory SQLite VFS with multi-version
// concurrency control. Each open file is backed by a Go-side page
// store; concurrent readers see a stable snapshot taken at lock-time
// while one writer mutates a private working set, atomically replacing
// the live snapshot on commit.
//
// Use cases:
//
//   - **Fast in-memory tests** that need to survive multiple `*sql.DB`
//     opens (named DBs are shared across handles by name).
//   - **Cold-cache workloads** where the active dataset fits in RAM and
//     you want consistent reads while a writer is committing.
//   - **Sandboxed experiments** — no disk I/O at all; the entire DB
//     lives in process memory and disappears when the VFS is closed.
//
// # Naming
//
// Database files are identified by URI path. Paths beginning with `/`
// are SHARED across all opens of the same VFS — multiple
// `sql.Open("sqlite", "file:/db?vfs=<name>")` calls see the same data.
// Paths without a leading `/` are PRIVATE: each open gets its own
// independent in-memory DB (useful for test isolation when the test
// pool size is unconstrained).
//
//	name, fs, _ := mvcc.New(mvcc.Options{})
//	defer fs.Close()
//	db1, _ := sql.Open("sqlite", "file:/shared.db?vfs="+name) // shared
//	db2, _ := sql.Open("sqlite", "file:scratch.db?vfs="+name) // private
//
// # Snapshot isolation
//
// A reader's view of the database is captured at the moment it
// acquires a SHARED lock; subsequent writes by other connections are
// not visible until the reader releases and re-acquires. A writer's
// modifications are buffered in a per-handle working set and atomically
// merged into the shared store on commit (xSync after EXCLUSIVE), so
// concurrent readers either see the pre-commit state or the
// post-commit state — never a partial mix.
//
// This is intentionally simpler than SQLite's full WAL mode: there is
// no log file, no checkpoint, no `-shm` index. Trade-offs:
//
//   - No durability — process exit drops everything.
//   - No multi-writer — one writer at a time per shared DB (BUSY
//     returned to losers).
//   - WAL mode (PRAGMA journal_mode=WAL) is NOT supported; the VFS
//     forces deletes on journal files and refuses to open them.
//
// # Limits
//
//   - Pages are stored as Go byte slices keyed by file offset. Each
//     write atomically replaces a page in the snapshot map; the cost
//     is proportional to dataset size, not write count, so large
//     databases pay a per-commit copy proportional to the page count.
//   - No checksum or encryption, and no `Options.WrapVFS` field —
//     mvcc owns the page store directly, so it cannot meaningfully
//     layer on top of (or beneath) the disk-oriented `vfs/cksm` /
//     `vfs/crypto` wrappers.
//
// Ported in spirit from [ncruces/vfs/mvcc] but without the upstream
// `wbt` red-black tree dependency. We use a simpler map + copy-on-
// write model since the typical SQLite workload fits its dataset
// keys-per-page evenly.
//
// [ncruces/vfs/mvcc]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/vfs/mvcc
package mvcc
