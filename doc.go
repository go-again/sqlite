// Package sqlite is a CGo-free SQLite driver for Go's database/sql.
// It is a drop-in replacement for both github.com/mattn/go-sqlite3
// (the dominant CGo-based driver) and modernc.org/sqlite (the
// upstream CGo-free wrapper this module is built on top of), and
// serves as the dialector source for the companion gosqlite.org/gorm module.
//
// # Modern Go-typed open (recommended)
//
// New code should reach for the structured [Config] entry. No DSN
// string assembly, no `_pragma=` URL flags to memorize, and a single
// defer Close that bundles the connection pool and any VFS teardown
// wired through [Config.VFSCloser]:
//
//	import sqlite "gosqlite.org"
//
//	db, err := sqlite.Open(sqlite.Config{
//	    Path:    "myapp.db",
//	    Pragmas: sqlite.RecommendedPragmas(), // WAL + busy_timeout=5s + foreign_keys
//	    MaxOpenConns: 8,
//	})
//	if err != nil { ... }
//	defer db.Close()
//
//	rows, _ := db.Query("SELECT ...") // *sql.DB methods, embedded
//
// PRAGMAs ride in via DSN `_pragma=` URL flags under the hood, so the
// driver applies them on every new connection in the pool — not just
// the one [database/sql] happens to pick for the first Exec.
//
// Encryption at rest takes the same Config shape via the
// gosqlite.org/vfs/crypto module's Open, which registers an encrypting
// VFS and bundles its teardown into db.Close():
//
//	db, _ := crypto.Open(
//	    sqlite.Config{Path: "secret.db", Pragmas: sqlite.RecommendedPragmas()},
//	    crypto.Options{Key: key}, // 32-byte Adiantum key (default cipher)
//	)
//
// crypto.DeriveKey turns a passphrase + salt into the right key length.
// The returned [*DB] embeds *sql.DB, so every database/sql method works
// unchanged.
//
// See [examples/getting-started/config] for the plain-Config demo and the
// gosqlite.org/gorm module's OpenConfig for the gorm flavor (same [Config]
// type, *gorm.DB return).
//
// # Driver registration (DSN form, still supported)
//
// Importing the package for side effects registers the driver under
// both names at once, so existing code using either keeps working:
//
//	import (
//	    "database/sql"
//
//	    _ "gosqlite.org"
//	)
//
//	db, _ := sql.Open("sqlite",  ":memory:") // modernc-style name
//	db, _ := sql.Open("sqlite3", ":memory:") // mattn-style name
//
// Both names resolve to the same singleton driver, so calling
// (*Driver).RegisterFunction / RegisterConnectionHook once affects
// every connection regardless of which name was used to open it.
//
// # Mattn-compatible surface
//
// The mattn drop-in is exhaustive — change the import path and
// existing code typically keeps compiling:
//
//   - Construction via &sqlite.SQLiteDriver{Extensions, ConnectHook}
//     (struct literal with the field names mattn used).
//   - Type aliases: SQLiteConn, SQLiteStmt, SQLiteRows, SQLiteTx,
//     SQLiteResult, SQLiteBackup, SQLiteError.
//   - Reflective RegisterFunc / RegisterAggregator with the same
//     call shape mattn exposes — variadic args, (T, error) returns,
//     pure-vs-deterministic UDF flag.
//   - DSN flag translation: every `_*` flag mattn supports
//     (_foreign_keys, _busy_timeout, _journal_mode, _txlock,
//     _time_format, …) lands as the equivalent PRAGMA. See dsn.go for
//     the full table; unknown flags surface a clear error rather than
//     being silently dropped.
//   - Error code introspection: (*Error).Code() / ExtendedCode() plus
//     the SQLITE_* / SQLITE_CONSTRAINT_* sentinels exposed in
//     constants.go. Works with errors.Is.
//
// The compat surface is enforced by tests we vendored from mattn's
// own suite — see dev/upstream/mattn.md and the `mattn_upstream`
// CI lane in the repo root.
//
// # Hooks and per-conn state
//
// Hooks that modernc never exposed but mattn users depend on are
// wired in via the same trampoline pattern modernc uses for the
// hooks it does expose:
//
//   - (*Conn).RegisterAuthorizer
//   - (*Conn).RegisterUpdateHook
//   - (*Conn).RegisterCommitHook / RegisterRollbackHook
//   - (*Conn).RegisterPreUpdateHook
//   - (*Conn).SetTrace
//   - (*Conn).Backup / SerializeSchema / Deserialize
//   - top-level Serialize(ctx, *sql.DB) → []byte
//
// Hooks are per-connection. To install one on a known *Conn, pin the
// pool to one with db.SetMaxOpenConns(1), grab a *sql.Conn, and use
// Conn.Raw to reach the underlying *sqlite.Conn. See examples/
// mattn-compat for the canonical pattern.
//
// # Quick start
//
//	package main
//
//	import (
//	    "database/sql"
//	    "errors"
//	    "fmt"
//
//	    sqlite "gosqlite.org"
//	)
//
//	func main() {
//	    sql.Register("with-udfs", &sqlite.SQLiteDriver{
//	        ConnectHook: func(c *sqlite.SQLiteConn) error {
//	            return c.RegisterFunc("double",
//	                func(x int64) int64 { return x * 2 }, true)
//	        },
//	    })
//
//	    db, _ := sql.Open("with-udfs",
//	        ":memory:?_foreign_keys=on&_busy_timeout=5000")
//	    defer db.Close()
//
//	    var v int64
//	    if err := db.QueryRow("SELECT double(21)").Scan(&v); err != nil {
//	        var se *sqlite.Error
//	        if errors.As(err, &se) {
//	            fmt.Println("sqlite code:", se.Code(), "ext:", se.ExtendedCode())
//	        }
//	    }
//	    fmt.Println(v) // 42
//	}
//
// # Sub-packages
//
// Higher-level capabilities live in sibling packages, each with its
// own doc:
//
//   - gosqlite.org/gorm — gorm dialector + Migrator (a SEPARATE
//     module; the core does not depend on gorm.io/gorm), drop-in for
//     gorm.io/driver/sqlite and glebarez/sqlite.
//   - gosqlite.org/vec — sqlite-vec vector search
//     (auto-registered extension + typed Go API).
//   - gosqlite.org/fts — typed FTS5 full-text search
//     (Index[K, V], query builder, tokenizers, BM25 + snippet /
//     highlight).
//   - gosqlite.org/fusion — rank-fusion helpers
//     (Reciprocal Rank Fusion) for combining vec.KNN and fts.Search
//     results into a single hybrid-search ranking.
//   - gosqlite.org/vfs — io/fs.FS-backed read-only
//     databases (e.g. opening a SQLite file out of an embed.FS). Also
//     exposes vfs.NewReader(io.ReaderAt, size) for the simpler
//     direct-buffer case.
//   - gosqlite.org/vfs/crypto — pure-Go encryption-at-rest
//     VFS (Adiantum or AES-XTS-256, transparent page-level encryption
//     of main DB + journal + WAL + temp files).
//   - gosqlite.org/vfs/cksm — pure-Go page-level checksum
//     VFS (Fletcher-style 8-byte trailer per page, on-disk compatible
//     with SQLite's cksumvfs). Composes beneath vfs/crypto.
//   - gosqlite.org/vfs/mvcc — in-memory MVCC VFS with
//     snapshot-isolation reads + atomic publish on commit; shared
//     (file:/name) and private (file:name) DBs.
//   - gosqlite.org/vfs/memdb — plain in-memory VFS with
//     direct per-page store, no MVCC; smaller-surface alternative to
//     vfs/mvcc for tests and scratch DBs.
//   - gosqlite.org/ext — opt-in loadable Go extensions:
//     array, blobio, bloom, closure, csv, fileio, hash, ipaddr, lines,
//     pivot, regexp, spellfix1, statement, stats, unicode, uuid,
//     zorder. Each sub-package is independent — pick what you need and
//     leave the rest off your import graph. Register per-conn via
//     <name>.Register(c) or pool-wide via blank-import of <name>/auto.
//     See dev/coverage/ext.md for the matrix.
//
// # Virtual tables from Go
//
// (*Conn).CreateModule and (*Conn).CreateEponymousModule expose
// Go-implemented virtual tables to SQLite. Implement the [VTab] and
// [VTabCursor] interfaces (plus optional [VTabUpdater] / [VTabRenamer] /
// [VTabTransactional]), then register a constructor that calls
// [Conn.DeclareVTab] inside its body. The eponymous variant lets the
// table be queried directly by its module name (e.g.
// `SELECT … FROM array(?)`) without a preceding CREATE VIRTUAL TABLE.
//
// # Custom pointer bindings
//
// [Pointer] wraps an arbitrary Go value so it can be bound as a SQL
// parameter and ferry through to a UDF's args slice or a vtab's Filter
// callback as the original Go object (rather than a SQLite primitive).
// SQLite drives the binding lifetime through a destructor callback — no
// caller-side Release is needed. See [ext/array] for the canonical use
// case.
//
// # SQLite version, libc pin
//
// The bundled SQLite is whatever build modernc.org/sqlite is pinned to
// in this module's go.mod. SQLite itself is not vendored or pinned by
// this module.
//
// modernc.org/libc is a hard ABI dependency of modernc.org/sqlite's
// transpiled C. Bumping one without the other breaks the generated
// code. If you redirect this module's deps in your own go.mod, keep
// the libc version aligned with what go.mod here declares. See
// https://gitlab.com/cznic/sqlite/-/issues/177 for context.
//
// # Supported platforms
//
// Coverage matches modernc.org/sqlite's transpilation matrix:
//
//	darwin   amd64, arm64
//	freebsd  amd64, arm64
//	linux    386, amd64, arm, arm64, loong64, ppc64le, riscv64, s390x
//	windows  386, amd64, arm64
//
// The vec sub-package is transpiled per-target by
// modernc.org/sqlite/vec and may skip some of these (see that
// package's build tags); fts and the core driver cover the full set.
package sqlite
