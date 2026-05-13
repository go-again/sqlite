// Package vfs exposes any Go io/fs.FS implementation as a read-only SQLite
// VFS, so an embed.FS or zip-backed FS can be opened as a database without
// touching the real filesystem.
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
//	    name, _, _ := vfs.New(seed)
//	    db, _ := sql.Open("sqlite3", "file:seed.db?vfs="+name)
//	    // ...
//	}
//
// This package is a thin re-export of modernc.org/sqlite/vfs; the underlying
// C bridge is platform-specific and transpiled per-target by modernc.
package vfs

import (
	"io/fs"

	mvfs "modernc.org/sqlite/vfs"
)

// FS is the type returned by New. It satisfies the SQLite VFS interface and
// holds the registered name for the lifetime of the program.
type FS = mvfs.FS

// New registers fs as a read-only SQLite VFS and returns the name to pass via
// the DSN ?vfs=<name> parameter. The returned *FS retains the VFS for the
// lifetime of the program.
//
// New is concurrency-safe; multiple calls register independent VFS instances.
// Open a database against this VFS by adding ?vfs=<name> to the DSN.
func New(f fs.FS) (string, *FS, error) {
	return mvfs.New(f)
}
