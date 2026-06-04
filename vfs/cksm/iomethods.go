package cksm

import (
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/go-again/sqlite/internal/cabi"
)

func defaultMethodsFor(pFile uintptr) *sqlite3.Tsqlite3_io_methods {
	fs := fsForFile(pFile)
	if fs == nil {
		return nil
	}
	return (*sqlite3.Tsqlite3_io_methods)(unsafe.Pointer(fs.perFileStateOf(pFile).defaultMethods))
}

func xCloseTrampoline(tls *libc.TLS, pFile uintptr) int32 {
	methods := defaultMethodsFor(pFile)
	unregisterFile(pFile)
	return cabi.CallXClose(tls, methods.FxClose, pFile)
}

// xReadTrampoline reads into buf, then — if checksumming is enabled
// and the read covered a full page — verifies the checksum trailer
// and returns SQLITE_IOERR_DATA on mismatch. The first xRead at off=0
// also primes pst.enabled from the SQLite header's reserved_bytes
// byte.
func xReadTrampoline(tls *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	rc := cabi.CallXRead(tls, defaultMethodsFor(pFile).FxRead, pFile, buf, amt, off)
	fs := fsForFile(pFile)
	if fs == nil {
		return sqlite3.SQLITE_IOERR
	}
	pst := fs.perFileStateOf(pFile)
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
	fs := fsForFile(pFile)
	if fs == nil {
		return sqlite3.SQLITE_IOERR
	}
	pst := fs.perFileStateOf(pFile)
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
	return cabi.CallXWrite(tls, defaultMethodsFor(pFile).FxWrite, pFile, buf, amt, off)
}

func xTruncateTrampoline(tls *libc.TLS, pFile uintptr, size sqlite3.Tsqlite3_int64) int32 {
	return cabi.CallXTruncate(tls, defaultMethodsFor(pFile).FxTruncate, pFile, size)
}

func xSyncTrampoline(tls *libc.TLS, pFile uintptr, flags int32) int32 {
	return cabi.CallXSync(tls, defaultMethodsFor(pFile).FxSync, pFile, flags)
}

func xFileSizeTrampoline(tls *libc.TLS, pFile, pSize uintptr) int32 {
	return cabi.CallXFileSize(tls, defaultMethodsFor(pFile).FxFileSize, pFile, pSize)
}

func xLockTrampoline(tls *libc.TLS, pFile uintptr, level int32) int32 {
	return cabi.CallXLock(tls, defaultMethodsFor(pFile).FxLock, pFile, level)
}

func xUnlockTrampoline(tls *libc.TLS, pFile uintptr, level int32) int32 {
	return cabi.CallXLock(tls, defaultMethodsFor(pFile).FxUnlock, pFile, level)
}

func xCheckReservedLockTrampoline(tls *libc.TLS, pFile, pResOut uintptr) int32 {
	return cabi.CallXCheckReservedLock(tls, defaultMethodsFor(pFile).FxCheckReservedLock, pFile, pResOut)
}

func xFileControlTrampoline(tls *libc.TLS, pFile uintptr, op int32, pArg uintptr) int32 {
	return cabi.CallXFileControl(tls, defaultMethodsFor(pFile).FxFileControl, pFile, op, pArg)
}

func xSectorSizeTrampoline(tls *libc.TLS, pFile uintptr) int32 {
	return cabi.CallXSectorSize(tls, defaultMethodsFor(pFile).FxSectorSize, pFile)
}

func xDeviceCharacteristicsTrampoline(tls *libc.TLS, pFile uintptr) int32 {
	return cabi.CallXSectorSize(tls, defaultMethodsFor(pFile).FxDeviceCharacteristics, pFile)
}

func xShmMapTrampoline(tls *libc.TLS, pFile uintptr, iPage, pgsz, bExtend int32, pp uintptr) int32 {
	return cabi.AsFunc[func(*libc.TLS, uintptr, int32, int32, int32, uintptr) int32](
		defaultMethodsFor(pFile).FxShmMap)(tls, pFile, iPage, pgsz, bExtend, pp)
}

func xShmLockTrampoline(tls *libc.TLS, pFile uintptr, offset, n, flags int32) int32 {
	return cabi.AsFunc[func(*libc.TLS, uintptr, int32, int32, int32) int32](
		defaultMethodsFor(pFile).FxShmLock)(tls, pFile, offset, n, flags)
}

func xShmBarrierTrampoline(tls *libc.TLS, pFile uintptr) {
	cabi.AsFunc[func(*libc.TLS, uintptr)](
		defaultMethodsFor(pFile).FxShmBarrier)(tls, pFile)
}

func xShmUnmapTrampoline(tls *libc.TLS, pFile uintptr, deleteFlag int32) int32 {
	return cabi.AsFunc[func(*libc.TLS, uintptr, int32) int32](
		defaultMethodsFor(pFile).FxShmUnmap)(tls, pFile, deleteFlag)
}

// All consumer-side function-pointer casts go through cabi.CallX*; see
// internal/cabi/callx.go.
