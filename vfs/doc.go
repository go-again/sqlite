// Package vfs exposes any Go io/fs.FS implementation as a read-only
// SQLite VFS. Pair it with the parent github.com/go-again/sqlite
// driver to open a SQLite database backed by an embed.FS, a
// fstest.MapFS, a zip-backed fs.FS, or any other read-only
// filesystem implementation — without ever touching the real disk.
//
// # When to use this
//
// The canonical use case is shipping seed data inside the binary.
// A CLI tool that wants to read from a pre-populated SQLite catalog
// can embed the database file in its own binary, register it as a
// VFS, and open it as a normal *sql.DB:
//
//	import (
//	    "embed"
//	    "database/sql"
//	    _ "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/vfs"
//	)
//
//	//go:embed seed.db
//	var seed embed.FS
//
//	func main() {
//	    name, _, err := vfs.New(seed)
//	    if err != nil { ... }
//	    db, err := sql.Open("sqlite3",
//	        "file:seed.db?vfs="+name+"&mode=ro")
//	    if err != nil { ... }
//	    defer db.Close()
//	    // db is now a read-only handle on the embedded file.
//	}
//
// # API
//
// The package exposes exactly two symbols:
//
//   - New(fs.FS) (name string, *FS, error) — registers fs under a
//     fresh unique VFS name and returns it. Pass the name back via
//     the DSN's ?vfs= parameter when opening a database.
//   - FS — a type alias for modernc.org/sqlite/vfs.FS, kept around
//     so callers can hold a reference to the registered VFS for
//     lifetime control if needed.
//
// New is concurrency-safe; multiple calls register independent VFS
// instances with distinct names.
//
// # Read-only by design
//
// The VFS is read-only. Any write the database engine would attempt
// (journals, locks, the file itself) is rejected by the underlying
// fs.FS contract. Append &mode=ro to the DSN to make SQLite refuse
// writes at open time — that gives you a clear error rather than a
// surprise mid-query.
//
// # Implementation
//
// This package is a thin re-export of modernc.org/sqlite/vfs. The C
// side of the VFS bridge is transpiled per-target by that package;
// platform coverage matches modernc.org/sqlite's coverage matrix.
//
// # See also
//
//   - examples/vfs-embed/ — minimal embed.FS demonstration.
//   - examples/gorm-vfs/ — same flow with a gorm dialector on top.
package vfs
