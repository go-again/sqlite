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
