package vfs

import (
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"gosqlite.org/internal/cabi"
)

// baseIoMethods returns the version-1 io-methods table with every non-shm slot
// wired. initIoMethods stores it as-is; initShmIoMethods (shm.go) starts from it,
// bumps FiVersion to 2, and fills the FxShm* slots — so the two tables share one
// definition of the base trampolines and a rename cannot update one and skip the
// other.
func baseIoMethods() sqlite3.Tsqlite3_io_methods {
	return sqlite3.Tsqlite3_io_methods{
		FiVersion:               1,
		FxClose:                 cabi.FuncPointer(xCloseTrampoline),
		FxRead:                  cabi.FuncPointer(xReadTrampoline),
		FxWrite:                 cabi.FuncPointer(xWriteTrampoline),
		FxTruncate:              cabi.FuncPointer(xTruncateTrampoline),
		FxSync:                  cabi.FuncPointer(xSyncTrampoline),
		FxFileSize:              cabi.FuncPointer(xFileSizeTrampoline),
		FxLock:                  cabi.FuncPointer(xLockTrampoline),
		FxUnlock:                cabi.FuncPointer(xUnlockTrampoline),
		FxCheckReservedLock:     cabi.FuncPointer(xCheckReservedLockTrampoline),
		FxFileControl:           cabi.FuncPointer(xFileControlTrampoline),
		FxSectorSize:            cabi.FuncPointer(xSectorSizeTrampoline),
		FxDeviceCharacteristics: cabi.FuncPointer(xDeviceCharacteristicsTrampoline),
	}
}

// initIoMethods builds the single, shared io-methods table every file
// opened through any user VFS points at. FiVersion 1 omits the
// shared-memory (xShm*) slots — WAL is Phase 2 — so SQLite keeps a
// custom-VFS database in rollback-journal mode.
func initIoMethods() {
	ioMethods = baseIoMethods()
	ioMethodsPtr.Store(uintptr(unsafe.Pointer(&ioMethods)))
}

func xCloseTrampoline(_ *libc.TLS, pFile uintptr) int32 {
	pst := perFileStateOf(pFile)
	of := fileRegistry.Lookup(pst.token)
	if of == nil {
		return sqlite3.SQLITE_OK
	}
	fileRegistry.Unregister(pst.token)
	pst.token = 0

	// SQLite normally calls xShmUnmap before xClose, but detach again as
	// a safety net so an abnormal teardown can't leak a shm group.
	of.detachShm()

	err := of.file.Close()
	// Files opened with DELETEONCLOSE must vanish on close; the native
	// os VFS unlinks them here, so the dispatcher does too (best effort,
	// matching SQLite which ignores the delete error at this point).
	if of.flags.Has(OpenDeleteOnClose) && of.name != "" {
		_ = of.vfs.impl.Delete(of.name, false)
	}
	if err != nil {
		return codeOf(err, sqlite3.SQLITE_IOERR_CLOSE)
	}
	return sqlite3.SQLITE_OK
}

// xRead fills SQLite's C buffer via a fresh Go slice handed to
// File.ReadAt (never aliasing C memory into Go past the call). A read
// that comes up short past EOF zero-fills the tail and reports
// SHORT_READ — the contract SQLite relies on to detect a freshly
// created or extended database file.
func xReadTrampoline(_ *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	of := fileFor(pFile)
	if of == nil {
		return sqlite3.SQLITE_IOERR_READ
	}
	p := make([]byte, amt)
	n, err := of.file.ReadAt(p, int64(off))
	if n < 0 {
		n = 0
	}
	if n > int(amt) {
		n = int(amt)
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(amt))
	copy(dst, p[:n])
	if n < int(amt) {
		for i := n; i < int(amt); i++ {
			dst[i] = 0
		}
		return sqlite3.SQLITE_IOERR_SHORT_READ
	}
	if err != nil {
		return codeOf(err, sqlite3.SQLITE_IOERR_READ)
	}
	return sqlite3.SQLITE_OK
}

// xWrite copies SQLite's C buffer into a fresh Go slice before handing
// it to File.WriteAt, so the implementation may retain the page.
func xWriteTrampoline(_ *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	of := fileFor(pFile)
	if of == nil {
		return sqlite3.SQLITE_IOERR_WRITE
	}
	src := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(amt))
	p := make([]byte, amt)
	copy(p, src)
	n, err := of.file.WriteAt(p, int64(off))
	if err != nil {
		return codeOf(err, sqlite3.SQLITE_IOERR_WRITE)
	}
	if n < int(amt) {
		return sqlite3.SQLITE_IOERR_WRITE
	}
	return sqlite3.SQLITE_OK
}

func xTruncateTrampoline(_ *libc.TLS, pFile uintptr, size sqlite3.Tsqlite3_int64) int32 {
	of := fileFor(pFile)
	if of == nil {
		return sqlite3.SQLITE_IOERR_TRUNCATE
	}
	if err := of.file.Truncate(int64(size)); err != nil {
		return codeOf(err, sqlite3.SQLITE_IOERR_TRUNCATE)
	}
	return sqlite3.SQLITE_OK
}

func xSyncTrampoline(_ *libc.TLS, pFile uintptr, flags int32) int32 {
	of := fileFor(pFile)
	if of == nil {
		return sqlite3.SQLITE_IOERR_FSYNC
	}
	if err := of.file.Sync(SyncFlags(flags)); err != nil {
		return codeOf(err, sqlite3.SQLITE_IOERR_FSYNC)
	}
	return sqlite3.SQLITE_OK
}

func xFileSizeTrampoline(_ *libc.TLS, pFile, pSize uintptr) int32 {
	of := fileFor(pFile)
	if of == nil {
		return sqlite3.SQLITE_IOERR_FSTAT
	}
	sz, err := of.file.Size()
	if err != nil {
		return codeOf(err, sqlite3.SQLITE_IOERR_FSTAT)
	}
	*(*sqlite3.Tsqlite3_int64)(unsafe.Pointer(pSize)) = sqlite3.Tsqlite3_int64(sz)
	return sqlite3.SQLITE_OK
}

func xLockTrampoline(_ *libc.TLS, pFile uintptr, level int32) int32 {
	of := fileFor(pFile)
	if of == nil {
		return sqlite3.SQLITE_IOERR_LOCK
	}
	if err := of.file.Lock(LockLevel(level)); err != nil {
		return codeOf(err, sqlite3.SQLITE_IOERR_LOCK)
	}
	return sqlite3.SQLITE_OK
}

func xUnlockTrampoline(_ *libc.TLS, pFile uintptr, level int32) int32 {
	of := fileFor(pFile)
	if of == nil {
		return sqlite3.SQLITE_IOERR_UNLOCK
	}
	if err := of.file.Unlock(LockLevel(level)); err != nil {
		return codeOf(err, sqlite3.SQLITE_IOERR_UNLOCK)
	}
	return sqlite3.SQLITE_OK
}

func xCheckReservedLockTrampoline(_ *libc.TLS, pFile, pResOut uintptr) int32 {
	of := fileFor(pFile)
	if of == nil {
		return sqlite3.SQLITE_IOERR_CHECKRESERVEDLOCK
	}
	held, err := of.file.CheckReservedLock()
	res := int32(0)
	if err == nil && held {
		res = 1
	}
	if pResOut != 0 {
		*(*int32)(unsafe.Pointer(pResOut)) = res
	}
	if err != nil {
		return codeOf(err, sqlite3.SQLITE_IOERR_CHECKRESERVEDLOCK)
	}
	return sqlite3.SQLITE_OK
}

func xFileControlTrampoline(_ *libc.TLS, pFile uintptr, op int32, pArg uintptr) int32 {
	of := fileFor(pFile)
	if of == nil {
		return sqlite3.SQLITE_NOTFOUND
	}
	fc, ok := of.file.(FileControl)
	if !ok {
		return sqlite3.SQLITE_NOTFOUND
	}
	if err := fc.FileControl(int(op), pArg); err != nil {
		return codeOf(err, sqlite3.SQLITE_ERROR)
	}
	return sqlite3.SQLITE_OK
}

func xSectorSizeTrampoline(_ *libc.TLS, pFile uintptr) int32 {
	of := fileFor(pFile)
	if of == nil {
		return 4096
	}
	if s := of.file.SectorSize(); s > 0 {
		return int32(s)
	}
	return 4096
}

func xDeviceCharacteristicsTrampoline(_ *libc.TLS, pFile uintptr) int32 {
	of := fileFor(pFile)
	if of == nil {
		return 0
	}
	return int32(of.file.DeviceCharacteristics())
}
