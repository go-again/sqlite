// Package sqlite is a CGo-free SQLite driver for Go's database/sql.
// It is a drop-in replacement for both github.com/mattn/go-sqlite3
// (the dominant CGo-based driver) and modernc.org/sqlite (the
// upstream CGo-free wrapper this fork is built on top of), and serves
// as the dialector source for the sibling gorm package.
//
// # Driver registration
//
// Importing the package for side effects registers the driver under
// both names at once, so existing code using either keeps working:
//
//	import (
//	    "database/sql"
//
//	    _ "github.com/go-again/sqlite"
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
//	- Construction via &sqlite.SQLiteDriver{Extensions, ConnectHook}
//	  (struct literal with the field names mattn used).
//	- Type aliases: SQLiteConn, SQLiteStmt, SQLiteRows, SQLiteTx,
//	  SQLiteResult, SQLiteBackup, SQLiteError.
//	- Reflective RegisterFunc / RegisterAggregator with the same
//	  call shape mattn exposes — variadic args, (T, error) returns,
//	  pure-vs-deterministic UDF flag.
//	- DSN flag translation: every `_*` flag mattn supports
//	  (_foreign_keys, _busy_timeout, _journal_mode, _txlock,
//	  _time_format, …) lands as the equivalent PRAGMA. See dsn.go for
//	  the full table; unknown flags surface a clear error rather than
//	  being silently dropped.
//	- Error code introspection: (*Error).Code() / ExtendedCode() plus
//	  the SQLITE_* / SQLITE_CONSTRAINT_* sentinels exposed in
//	  constants.go. Works with errors.Is.
//
// The compat surface is enforced by tests we vendored from mattn's
// own suite — see docs/mattn-upstream.md and the `mattn_upstream`
// CI lane in the repo root.
//
// # Hooks and per-conn state
//
// Hooks that modernc never exposed but mattn users depend on are
// wired in via the same trampoline pattern modernc uses for the
// hooks it does expose:
//
//	- (*Conn).RegisterAuthorizer
//	- (*Conn).RegisterUpdateHook
//	- (*Conn).RegisterCommitHook / RegisterRollbackHook
//	- (*Conn).RegisterPreUpdateHook
//	- (*Conn).SetTrace
//	- (*Conn).Backup / SerializeNoCopy / Deserialize
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
//	    sqlite "github.com/go-again/sqlite"
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
//   - github.com/go-again/sqlite/gorm — gorm dialector + Migrator,
//     drop-in for gorm.io/driver/sqlite and glebarez/sqlite.
//   - github.com/go-again/sqlite/vec — sqlite-vec vector search
//     (auto-registered extension + typed Go API).
//   - github.com/go-again/sqlite/vec/gorm — tag-driven vec sidecars
//     wired into gorm models.
//   - github.com/go-again/sqlite/fts — typed FTS5 full-text search
//     (Index[K, V], query builder, tokenizers, BM25 + snippet /
//     highlight).
//   - github.com/go-again/sqlite/fts/gorm — tag-driven FTS5 indexes
//     wired into gorm models (external / in-table / contentless
//     modes).
//   - github.com/go-again/sqlite/vfs — io/fs.FS-backed read-only
//     databases (e.g. opening a SQLite file out of an embed.FS).
//
// # SQLite version, libc pin
//
// The bundled SQLite is whatever version modernc.org/sqlite is pinned
// to in this module's go.mod. Currently that's 3.53.1. We do not pin
// or fork SQLite itself.
//
// modernc.org/libc is a hard ABI dependency of modernc.org/sqlite's
// transpiled C. Bumping one without the other breaks the generated
// code. If you redirect this module's deps in your own go.mod, keep
// the libc version aligned with what go.mod here declares. See
// https://gitlab.com/cznic/sqlite/-/issues/177 for context.
//
// # Supported platforms
//
// Coverage matches modernc.org/sqlite's matrix:
//
//	OS      Arch    SQLite
//	-------------------------
//	darwin   amd64   3.53.1
//	darwin   arm64   3.53.1
//	freebsd  amd64   3.53.1
//	freebsd  arm64   3.53.1
//	linux    386     3.53.1
//	linux    amd64   3.53.1
//	linux    arm     3.53.1
//	linux    arm64   3.53.1
//	linux    loong64 3.53.1
//	linux    ppc64le 3.53.1
//	linux    riscv64 3.53.1
//	linux    s390x   3.53.1
//	windows  386     3.53.1
//	windows  amd64   3.53.1
//	windows  arm64   3.53.1
//
// The vec sub-package is transpiled per-target by
// modernc.org/sqlite/vec and may skip some of these (see that
// package's build tags); fts and the core driver cover the full set.
package sqlite
