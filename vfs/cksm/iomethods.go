package cksm

import (
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

func defaultMethodsFor(pFile uintptr) *sqlite3.Tsqlite3_io_methods {
	return (*sqlite3.Tsqlite3_io_methods)(unsafe.Pointer(perFileStateOf(pFile).defaultMethods))
}

func xCloseTrampoline(tls *libc.TLS, pFile uintptr) int32 {
	return callXClose(tls, defaultMethodsFor(pFile).FxClose, pFile)
}

// xReadTrampoline reads into buf, then — if checksumming is enabled
// and the read covered a full page — verifies the checksum trailer
// and returns SQLITE_IOERR_DATA on mismatch. The first xRead at off=0
// also primes pst.enabled from the SQLite header's reserved_bytes
// byte.
func xReadTrampoline(tls *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	rc := callXRead(tls, defaultMethodsFor(pFile).FxRead, pFile, buf, amt, off)
	pst := perFileStateOf(pFile)
	if pst.isMain == 0 {
		return rc
	}
	if rc != sqlite3.SQLITE_OK && rc != sqlite3.SQLITE_IOERR_SHORT_READ {
		return rc
	}
	page := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(amt))
	if off == 0 {
		initFromHeader(pst, page)
	}
	if pst.enabled.Load() == 0 || amt != pst.pageSize {
		return rc
	}
	// SHORT_READ on a full-page request means the file ended mid-page;
	// no checksum to verify on the partial tail. Forward the rc.
	if rc == sqlite3.SQLITE_IOERR_SHORT_READ {
		return rc
	}
	want := compute(page[:len(page)-8])
	var got [8]byte
	copy(got[:], page[len(page)-8:])
	if want != got {
		return sqlite3.SQLITE_IOERR_DATA
	}
	return rc
}

// xWriteTrampoline computes and stamps the checksum trailer into the
// page just before writing, when checksumming is enabled and the
// write covers a full page. The first write at offset 0 also primes
// pst.enabled from the SQLite header.
func xWriteTrampoline(tls *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	pst := perFileStateOf(pFile)
	if pst.isMain != 0 {
		page := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(amt))
		if off == 0 {
			initFromHeader(pst, page)
		}
		if pst.enabled.Load() != 0 && amt == pst.pageSize {
			cksm := compute(page[:len(page)-8])
			copy(page[len(page)-8:], cksm[:])
		}
	}
	return callXWrite(tls, defaultMethodsFor(pFile).FxWrite, pFile, buf, amt, off)
}

func xTruncateTrampoline(tls *libc.TLS, pFile uintptr, size sqlite3.Tsqlite3_int64) int32 {
	return callXTruncate(tls, defaultMethodsFor(pFile).FxTruncate, pFile, size)
}

func xSyncTrampoline(tls *libc.TLS, pFile uintptr, flags int32) int32 {
	return callXSync(tls, defaultMethodsFor(pFile).FxSync, pFile, flags)
}

func xFileSizeTrampoline(tls *libc.TLS, pFile, pSize uintptr) int32 {
	return callXFileSize(tls, defaultMethodsFor(pFile).FxFileSize, pFile, pSize)
}

func xLockTrampoline(tls *libc.TLS, pFile uintptr, level int32) int32 {
	return callXLock(tls, defaultMethodsFor(pFile).FxLock, pFile, level)
}

func xUnlockTrampoline(tls *libc.TLS, pFile uintptr, level int32) int32 {
	return callXLock(tls, defaultMethodsFor(pFile).FxUnlock, pFile, level)
}

func xCheckReservedLockTrampoline(tls *libc.TLS, pFile, pResOut uintptr) int32 {
	return callXFileSize(tls, defaultMethodsFor(pFile).FxCheckReservedLock, pFile, pResOut)
}

func xFileControlTrampoline(tls *libc.TLS, pFile uintptr, op int32, pArg uintptr) int32 {
	return callXFileControl(tls, defaultMethodsFor(pFile).FxFileControl, pFile, op, pArg)
}

func xSectorSizeTrampoline(tls *libc.TLS, pFile uintptr) int32 {
	return callXSectorSize(tls, defaultMethodsFor(pFile).FxSectorSize, pFile)
}

func xDeviceCharacteristicsTrampoline(tls *libc.TLS, pFile uintptr) int32 {
	return callXSectorSize(tls, defaultMethodsFor(pFile).FxDeviceCharacteristics, pFile)
}

func xShmMapTrampoline(tls *libc.TLS, pFile uintptr, iPage, pgsz, bExtend int32, pp uintptr) int32 {
	return asFunc[func(*libc.TLS, uintptr, int32, int32, int32, uintptr) int32](
		defaultMethodsFor(pFile).FxShmMap)(tls, pFile, iPage, pgsz, bExtend, pp)
}

func xShmLockTrampoline(tls *libc.TLS, pFile uintptr, offset, n, flags int32) int32 {
	return asFunc[func(*libc.TLS, uintptr, int32, int32, int32) int32](
		defaultMethodsFor(pFile).FxShmLock)(tls, pFile, offset, n, flags)
}

func xShmBarrierTrampoline(tls *libc.TLS, pFile uintptr) {
	asFunc[func(*libc.TLS, uintptr)](
		defaultMethodsFor(pFile).FxShmBarrier)(tls, pFile)
}

func xShmUnmapTrampoline(tls *libc.TLS, pFile uintptr, deleteFlag int32) int32 {
	return asFunc[func(*libc.TLS, uintptr, int32) int32](
		defaultMethodsFor(pFile).FxShmUnmap)(tls, pFile, deleteFlag)
}

// asFunc mirrors the helper in vfs/crypto: turn a stored uintptr back
// into a callable Go function value of the requested signature. See
// vfs/crypto/iomethods.go for the full design notes.
func asFunc[F any](fp uintptr) F {
	return *(*F)(unsafe.Pointer(&struct{ uintptr }{fp}))
}

func callXClose(tls *libc.TLS, fp, pFile uintptr) int32 {
	return asFunc[func(*libc.TLS, uintptr) int32](fp)(tls, pFile)
}

func callXRead(tls *libc.TLS, fp, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	return asFunc[func(*libc.TLS, uintptr, uintptr, int32, sqlite3.Tsqlite3_int64) int32](fp)(tls, pFile, buf, amt, off)
}

func callXWrite(tls *libc.TLS, fp, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	return asFunc[func(*libc.TLS, uintptr, uintptr, int32, sqlite3.Tsqlite3_int64) int32](fp)(tls, pFile, buf, amt, off)
}

func callXTruncate(tls *libc.TLS, fp, pFile uintptr, size sqlite3.Tsqlite3_int64) int32 {
	return asFunc[func(*libc.TLS, uintptr, sqlite3.Tsqlite3_int64) int32](fp)(tls, pFile, size)
}

func callXSync(tls *libc.TLS, fp, pFile uintptr, flags int32) int32 {
	return asFunc[func(*libc.TLS, uintptr, int32) int32](fp)(tls, pFile, flags)
}

func callXFileSize(tls *libc.TLS, fp, pFile, pSize uintptr) int32 {
	return asFunc[func(*libc.TLS, uintptr, uintptr) int32](fp)(tls, pFile, pSize)
}

func callXLock(tls *libc.TLS, fp, pFile uintptr, level int32) int32 {
	return asFunc[func(*libc.TLS, uintptr, int32) int32](fp)(tls, pFile, level)
}

func callXFileControl(tls *libc.TLS, fp, pFile uintptr, op int32, pArg uintptr) int32 {
	return asFunc[func(*libc.TLS, uintptr, int32, uintptr) int32](fp)(tls, pFile, op, pArg)
}

func callXSectorSize(tls *libc.TLS, fp, pFile uintptr) int32 {
	return asFunc[func(*libc.TLS, uintptr) int32](fp)(tls, pFile)
}
