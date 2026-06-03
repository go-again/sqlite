package vfs

import (
	"errors"
	"io"
	"io/fs"
	"time"

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

// NewReader registers an immutable single-file SQLite VFS backed by r,
// returning the DSN-ready VFS name and a handle. The caller MUST keep
// r alive (and its contents immutable) for as long as any database is
// open against the returned VFS. The reported file name within the VFS
// is "db" — open it via `?vfs=<name>&filename=db&mode=ro`, or just
// `file:db?vfs=<name>&mode=ro`.
//
// Common use: you have a database baked into memory (decompressed, or
// fetched from a network) and you want SQLite to read it without
// wrapping it in a synthetic fs.FS yourself. NewReader is the
// equivalent of ncruces' vfs/readervfs sub-package.
func NewReader(r io.ReaderAt, size int64) (string, *FS, error) {
	if r == nil {
		return "", nil, errors.New("vfs: NewReader: ReaderAt is nil")
	}
	if size < 0 {
		return "", nil, errors.New("vfs: NewReader: negative size")
	}
	return New(&readerFS{r: r, size: size})
}

// readerFS adapts an io.ReaderAt + size into a one-file fs.FS so
// modernc's existing VFS adapter can take over. The single file is
// named "db".
type readerFS struct {
	r    io.ReaderAt
	size int64
}

func (rf *readerFS) Open(name string) (fs.File, error) {
	if name != "db" && name != "." {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &readerFile{rf: rf}, nil
}

// readerFile is the fs.File for the synthetic "db" entry. It carries
// a cursor for sequential Read calls and exposes ReadAt for the
// hot-path random-access calls modernc's adapter prefers.
type readerFile struct {
	rf  *readerFS
	pos int64
}

func (f *readerFile) Stat() (fs.FileInfo, error) {
	return &readerInfo{size: f.rf.size}, nil
}

func (f *readerFile) Read(p []byte) (int, error) {
	if f.pos >= f.rf.size {
		return 0, io.EOF
	}
	n, err := f.rf.r.ReadAt(p, f.pos)
	f.pos += int64(n)
	return n, err
}

func (f *readerFile) ReadAt(p []byte, off int64) (int, error) {
	return f.rf.r.ReadAt(p, off)
}

func (f *readerFile) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = f.pos + offset
	case io.SeekEnd:
		abs = f.rf.size + offset
	default:
		return 0, errors.New("vfs: NewReader.Seek: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("vfs: NewReader.Seek: negative position")
	}
	f.pos = abs
	return abs, nil
}

func (f *readerFile) Close() error { return nil }

type readerInfo struct {
	size int64
}

func (i *readerInfo) Name() string       { return "db" }
func (i *readerInfo) Size() int64        { return i.size }
func (i *readerInfo) Mode() fs.FileMode  { return 0o444 }
func (i *readerInfo) ModTime() time.Time { return time.Time{} }
func (i *readerInfo) IsDir() bool        { return false }
func (i *readerInfo) Sys() any           { return nil }
