package vfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"

	sqlite3 "modernc.org/sqlite/lib"
)

// VFSError carries an explicit SQLite result code out of a [VFS] or
// [File] method. Return one when the default error→code mapping (a
// plain error becomes SQLITE_IOERR) is too coarse — e.g. to surface
// SQLITE_BUSY from Lock, or SQLITE_READONLY from WriteAt.
//
//	return &vfs.VFSError{Code: sqlite.SQLITE_BUSY}
//
// Code should be one of the SQLITE_* result codes (extended codes are
// fine). Err, if set, is unwrapped by errors.Is/As.
type VFSError struct {
	Code int
	Err  error
}

func (e *VFSError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("vfs: sqlite code %d: %v", e.Code, e.Err)
	}
	return fmt.Sprintf("vfs: sqlite code %d", e.Code)
}

// Unwrap exposes the wrapped cause to errors.Is/As.
func (e *VFSError) Unwrap() error { return e.Err }

// Errno wraps a bare SQLite result code as a [VFSError].
func Errno(code int) *VFSError { return &VFSError{Code: code} }

// ErrNotFound is the sentinel a [FileControl] implementation returns for
// an opcode it does not handle; the dispatcher maps it to
// SQLITE_NOTFOUND so SQLite falls back to its default behaviour.
var ErrNotFound = Errno(sqlite3.SQLITE_NOTFOUND)

// codeOf resolves err to a SQLite result code. A *VFSError yields its
// explicit Code; io.EOF maps to SQLITE_IOERR_SHORT_READ; everything
// else falls through to fallback (the call-site's most specific
// SQLITE_IOERR_* variant). A nil err is SQLITE_OK.
func codeOf(err error, fallback int32) int32 {
	if err == nil {
		return sqlite3.SQLITE_OK
	}
	var ve *VFSError
	if errors.As(err, &ve) {
		return int32(ve.Code)
	}
	if errors.Is(err, io.EOF) {
		return sqlite3.SQLITE_IOERR_SHORT_READ
	}
	return fallback
}

// isNotExist reports whether err signals a missing file, honouring both
// a *VFSError wrapping fs.ErrNotExist and a bare fs.ErrNotExist.
func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
