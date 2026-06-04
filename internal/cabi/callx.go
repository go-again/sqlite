package cabi

import (
	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// The CallX* family wraps [AsFunc] for the C-side signatures of every
// [sqlite3.Tsqlite3_io_methods] slot. Two wrap-forward VFSes
// (vfs/crypto, vfs/cksm) both forward to these signatures verbatim,
// and the SQLite VFS contract makes the shapes stable. Lifting the
// specializations here avoids per-package duplication.

// CallXClose invokes the xClose slot: `int (tls, pFile) -> rc`.
func CallXClose(tls *libc.TLS, fp, pFile uintptr) int32 {
	return AsFunc[func(*libc.TLS, uintptr) int32](fp)(tls, pFile)
}

// CallXRead invokes the xRead slot.
func CallXRead(tls *libc.TLS, fp, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	return AsFunc[func(*libc.TLS, uintptr, uintptr, int32, sqlite3.Tsqlite3_int64) int32](fp)(tls, pFile, buf, amt, off)
}

// CallXWrite invokes the xWrite slot. Same signature as xRead.
func CallXWrite(tls *libc.TLS, fp, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	return AsFunc[func(*libc.TLS, uintptr, uintptr, int32, sqlite3.Tsqlite3_int64) int32](fp)(tls, pFile, buf, amt, off)
}

// CallXTruncate invokes the xTruncate slot.
func CallXTruncate(tls *libc.TLS, fp, pFile uintptr, size sqlite3.Tsqlite3_int64) int32 {
	return AsFunc[func(*libc.TLS, uintptr, sqlite3.Tsqlite3_int64) int32](fp)(tls, pFile, size)
}

// CallXSync invokes the xSync slot.
func CallXSync(tls *libc.TLS, fp, pFile uintptr, flags int32) int32 {
	return AsFunc[func(*libc.TLS, uintptr, int32) int32](fp)(tls, pFile, flags)
}

// CallXFileSize invokes the xFileSize slot.
func CallXFileSize(tls *libc.TLS, fp, pFile, pSize uintptr) int32 {
	return AsFunc[func(*libc.TLS, uintptr, uintptr) int32](fp)(tls, pFile, pSize)
}

// CallXCheckReservedLock invokes the xCheckReservedLock slot. The C
// signature `(tls, pFile, *int) -> int` is identical to xFileSize's
// shape; the named alias keeps the grep trail honest at trampoline
// call sites.
func CallXCheckReservedLock(tls *libc.TLS, fp, pFile, pResOut uintptr) int32 {
	return AsFunc[func(*libc.TLS, uintptr, uintptr) int32](fp)(tls, pFile, pResOut)
}

// CallXLock is shared by xLock and xUnlock — their C signatures are
// identical (tls, pFile, level → int32), so one helper covers both.
func CallXLock(tls *libc.TLS, fp, pFile uintptr, level int32) int32 {
	return AsFunc[func(*libc.TLS, uintptr, int32) int32](fp)(tls, pFile, level)
}

// CallXFileControl invokes the xFileControl slot.
func CallXFileControl(tls *libc.TLS, fp, pFile uintptr, op int32, pArg uintptr) int32 {
	return AsFunc[func(*libc.TLS, uintptr, int32, uintptr) int32](fp)(tls, pFile, op, pArg)
}

// CallXSectorSize invokes the xSectorSize slot. xDeviceCharacteristics
// has the identical (tls, pFile → int32) shape, so CallXSectorSize
// is reused for both trampolines.
func CallXSectorSize(tls *libc.TLS, fp, pFile uintptr) int32 {
	return AsFunc[func(*libc.TLS, uintptr) int32](fp)(tls, pFile)
}

// CallXOpen invokes the xOpen slot on a wrapped VFS (not io-methods,
// but the only other forward-call shape both wrap-forward VFSes
// share).
func CallXOpen(tls *libc.TLS, fp, pVfs, zName, pFile uintptr, flags int32, pOutFlags uintptr) int32 {
	return AsFunc[func(*libc.TLS, uintptr, uintptr, uintptr, int32, uintptr) int32](fp)(tls, pVfs, zName, pFile, flags, pOutFlags)
}
