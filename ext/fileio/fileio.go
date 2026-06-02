// Package fileio exposes SQL functions and a virtual table for reading
// and walking files. It mirrors the SQLite fileio.c misc extension and
// ncruces/ext/fileio.
//
// # Functions and modules
//
//   - readfile(path) → BLOB: returns the contents of path, or NULL if the
//     file does not exist. Errors on other I/O failures.
//
//   - writefile(path, data [, mode]) → INTEGER: writes data to path,
//     returning the number of bytes written. mode is interpreted as a Go
//     [io/fs.FileMode] integer. Creates intermediate directories as
//     needed.
//
//   - lsmode(mode_int) → TEXT: the 10-character ls(1)-style rendering of
//     a [io/fs.FileMode] integer (e.g. "drwxr-xr-x" for 0o20000000755).
//
//   - fsdir(root [, depth]) virtual table: recursively walks root.
//     Columns:
//
//	    name   TEXT     – path relative to root
//	    mode   INTEGER  – io/fs.FileMode bits
//	    mtime  INTEGER  – modification time, Unix nanoseconds
//	    data   BLOB     – file contents for regular files; symlink target
//	                       for symlinks (os-backed only); NULL otherwise
//	    level  INTEGER  – walk depth (1 for direct children of root)
//	    path   HIDDEN   – initial-root constraint (required)
//	    depth  HIDDEN   – optional max depth constraint
//
// # Sandboxing
//
// Two registration modes:
//
//   - [Register] — os-backed. readfile, writefile, lsmode, and fsdir all
//     touch the local filesystem. Suitable when SQL is trusted (your own
//     code) but a security boundary when SQL is untrusted (LLM agents,
//     user-supplied queries).
//
//   - [RegisterFS] — sandboxed. Reads go through the supplied [io/fs.FS];
//     writefile is omitted entirely. Mirrors ext/lines' two-mode shape;
//     suitable for embedding read-only assets via embed.FS or for
//     untrusted SQL over a fstest.MapFS.
//
// Ported from [ncruces/ext/fileio]. Constraint planner support in fsdir is
// scoped to the root and depth columns; the ncruces "base" path-prefix
// optimization is deferred.
//
// [ncruces/ext/fileio]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/fileio
package fileio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	sqlite "github.com/go-again/sqlite"
)

// Register installs readfile, writefile, lsmode, and the fsdir vtab on c
// using the local filesystem. SQL written using these functions can read
// and write any path the host process can access; use [RegisterFS] for a
// sandboxed variant.
func Register(c *sqlite.Conn) error {
	return register(c, nil)
}

// RegisterFS installs readfile, lsmode, and the fsdir vtab on c, scoped to
// fsys. writefile is intentionally not registered — fs.FS is read-only.
// Pass fsys=nil for the os-backed mode (equivalent to [Register]).
func RegisterFS(c *sqlite.Conn, fsys fs.FS) error {
	return register(c, fsys)
}

func register(c *sqlite.Conn, fsys fs.FS) error {
	errs := []error{
		c.RegisterFunc("readfile", makeReadfile(fsys), false),
		c.RegisterFunc("lsmode", lsmode, true),
		c.CreateEponymousModule("fsdir", func(_ *sqlite.Conn, _, _, _ string, _ []string) (sqlite.VTab, error) {
			if err := c.DeclareVTab(`CREATE TABLE x(name TEXT, mode INTEGER, mtime INTEGER, data BLOB, level INTEGER, path HIDDEN, depth HIDDEN)`); err != nil {
				return nil, err
			}
			return &fsdirTable{fsys: fsys}, nil
		}),
	}
	if fsys == nil {
		errs = append(errs, c.RegisterFunc("writefile", writefile, false))
	}
	return errors.Join(errs...)
}

// lsmode renders a Go fs.FileMode integer as the 10-character ls(1) form.
func lsmode(mode int64) string {
	return fs.FileMode(mode).String()
}

func makeReadfile(fsys fs.FS) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		var data []byte
		var err error
		if fsys != nil {
			data, err = fs.ReadFile(fsys, name)
		} else {
			data, err = os.ReadFile(name)
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("readfile: %w", err)
		}
		return data, nil
	}
}

// writefile is the os-backed scalar. Writes data to path, creating
// intermediate dirs if absent. Returns bytes written.
//
// Signatures: writefile(path, data) or writefile(path, data, mode).
func writefile(name string, data []byte, mode ...int64) (int64, error) {
	perm := fs.FileMode(0o666)
	if len(mode) > 0 && mode[0] != 0 {
		perm = fs.FileMode(mode[0]).Perm()
	}
	if err := os.WriteFile(name, data, perm); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if mkErr := os.MkdirAll(filepath.Dir(name), 0o777); mkErr == nil {
				err = os.WriteFile(name, data, perm)
			}
		}
		if err != nil {
			return 0, fmt.Errorf("writefile: %w", err)
		}
	}
	return int64(len(data)), nil
}

// fsdirTable is the read-only virtual table returned by fsdir().
type fsdirTable struct {
	fsys fs.FS
}

// Column indices for the fsdir schema.
const (
	colName  = 0
	colMode  = 1
	colMtime = 2
	colData  = 3
	colLevel = 4
	colPath  = 5
	colDepth = 6
)

// IdxNum bit layout:
//
//	bit 0: depth constraint present (consumes arg[1])
func (t *fsdirTable) BestIndex(info *sqlite.IndexInfo) error {
	var pathIdx = -1
	var depthIdx = -1
	for i, cst := range info.Constraints {
		if !cst.Usable {
			continue
		}
		switch cst.Column {
		case colPath:
			if cst.Op == sqlite.OpEQ {
				pathIdx = i
			}
		case colDepth:
			if cst.Op == sqlite.OpEQ {
				depthIdx = i
			}
		}
	}
	if pathIdx < 0 {
		return errors.New("fsdir: missing required path=? constraint")
	}
	info.Constraints[pathIdx].ArgIndex = 0
	info.Constraints[pathIdx].Omit = true
	if depthIdx >= 0 {
		info.Constraints[depthIdx].ArgIndex = 1
		info.Constraints[depthIdx].Omit = true
		info.IdxNum = 1
	}
	info.EstimatedCost = 1e6
	info.EstimatedRows = 1000
	return nil
}

func (t *fsdirTable) Open() (sqlite.VTabCursor, error) {
	return &fsdirCursor{fsys: t.fsys}, nil
}

func (*fsdirTable) Disconnect() error { return nil }
func (*fsdirTable) Destroy() error    { return nil }

type fsdirEntry struct {
	relName string
	mode    fs.FileMode
	mtime   int64
	data    []byte
	level   int
	realErr error
}

type fsdirCursor struct {
	fsys    fs.FS
	entries []fsdirEntry
	row     int
	eof     bool
}

func (c *fsdirCursor) Filter(idxNum int, _ string, args []sqlite.Value) error {
	if len(args) < 1 {
		return errors.New("fsdir: missing root argument")
	}
	root, ok := args[0].(string)
	if !ok {
		return fmt.Errorf("fsdir: root must be TEXT, got %T", args[0])
	}
	maxDepth := -1
	if idxNum&1 != 0 {
		if len(args) < 2 {
			return errors.New("fsdir: missing depth argument")
		}
		switch v := args[1].(type) {
		case int64:
			maxDepth = int(v)
		case nil:
			// no constraint
		default:
			return fmt.Errorf("fsdir: depth must be INTEGER, got %T", args[1])
		}
	}

	c.entries = c.entries[:0]
	c.row = 0
	c.eof = false

	walk := func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			c.entries = append(c.entries, fsdirEntry{relName: p, realErr: walkErr})
			return nil
		}
		rel := strings.TrimPrefix(p, root)
		// fs.FS paths use '/' regardless of OS; filepath.WalkDir uses
		// the host separator. Pick the right one — counting both
		// double-counts on POSIX where the two are identical.
		sep := "/"
		if c.fsys == nil {
			sep = string(filepath.Separator)
		}
		rel = strings.TrimPrefix(rel, sep)
		level := 1 + strings.Count(rel, sep)
		if rel == "" {
			level = 0
		}
		if maxDepth >= 0 && level > maxDepth {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		ent := fsdirEntry{relName: rel, level: level}
		if d != nil {
			info, ierr := d.Info()
			if ierr == nil {
				ent.mode = info.Mode()
				ent.mtime = info.ModTime().UnixNano()
				if info.Mode().IsRegular() {
					if c.fsys != nil {
						b, rerr := fs.ReadFile(c.fsys, p)
						if rerr == nil {
							ent.data = b
						}
					} else {
						b, rerr := os.ReadFile(p)
						if rerr == nil {
							ent.data = b
						}
					}
				} else if info.Mode()&fs.ModeSymlink != 0 && c.fsys == nil {
					if target, rerr := os.Readlink(p); rerr == nil {
						ent.data = []byte(target)
					}
				}
			}
		}
		c.entries = append(c.entries, ent)
		return nil
	}

	if c.fsys != nil {
		root = path.Clean(root)
		return fs.WalkDir(c.fsys, root, walk)
	}
	root = filepath.Clean(root)
	return filepath.WalkDir(root, walk)
}

func (c *fsdirCursor) Next() error {
	c.row++
	if c.row >= len(c.entries) {
		c.eof = true
	}
	return nil
}

func (c *fsdirCursor) Eof() bool { return c.eof || c.row >= len(c.entries) }

func (c *fsdirCursor) Column(n int) (sqlite.Value, error) {
	if c.row >= len(c.entries) {
		return nil, io.EOF
	}
	e := &c.entries[c.row]
	if e.realErr != nil && n != colName {
		return nil, e.realErr
	}
	switch n {
	case colName:
		return e.relName, nil
	case colMode:
		return int64(e.mode), nil
	case colMtime:
		return e.mtime, nil
	case colData:
		if e.data == nil {
			return nil, nil
		}
		return e.data, nil
	case colLevel:
		return int64(e.level), nil
	case colPath, colDepth:
		return nil, nil
	}
	return nil, fmt.Errorf("fsdir: bad column index %d", n)
}

func (c *fsdirCursor) Rowid() (int64, error) { return int64(c.row), nil }
func (c *fsdirCursor) Close() error          { return nil }
