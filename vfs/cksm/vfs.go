package cksm

import (
	"bytes"
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/go-again/sqlite/internal/cabi"
)

// perFileState lives at the tail of each Tsqlite3_file allocation
// SQLite hands to our xOpen. The default unix VFS writes its own
// TunixFile into [0..defaultSzOsFile-1]; we tack our state on after
// it. Tsqlite3_vfs.FszOsFile is the sum of both at registration.
type perFileState struct {
	defaultMethods uintptr // pMethods value the default VFS installed
	fsToken        uintptr // registry handle for owning *FS; 0 = forward verbatim
	pageSize       int32   // cached page size for the hot path
	// enabled is 1 once the file's SQLite header has been inspected
	// and reserved_bytes==8 confirmed; 0 means pass-through.
	// atomic.Int32 because xRead and xWrite can run concurrently
	// against different pFile instances and the race detector would
	// flag a plain int32 write.
	enabled atomic.Int32
	isMain  int32   // 1 for main DB, 0 for everything else
	_       [4]byte // pad to 8-byte alignment
}

// initOnce guards package-level VFS state populated from the default
// VFS the first time New runs.
var initOnce sync.Once

var (
	defaultSzOsFile atomic.Int32
	defaultVfsPtr   atomic.Uintptr
	ourIoMethodsPtr atomic.Uintptr

	ourIoMethods sqlite3.Tsqlite3_io_methods
)

func initFromDefault(def *sqlite3.Tsqlite3_vfs) {
	defaultSzOsFile.Store(def.FszOsFile)
	defaultVfsPtr.Store(uintptr(unsafe.Pointer(def)))
	// iVersion=2 + FxShmMap non-zero so WAL works through our wrapper.
	// We forward every shm-related method 1:1 — checksums don't touch
	// the WAL index.
	ourIoMethods = sqlite3.Tsqlite3_io_methods{
		FiVersion:               2,
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
		FxShmMap:                cabi.FuncPointer(xShmMapTrampoline),
		FxShmLock:               cabi.FuncPointer(xShmLockTrampoline),
		FxShmBarrier:            cabi.FuncPointer(xShmBarrierTrampoline),
		FxShmUnmap:              cabi.FuncPointer(xShmUnmapTrampoline),
	}
	ourIoMethodsPtr.Store(uintptr(unsafe.Pointer(&ourIoMethods)))
}

func perFileStateOf(pFile uintptr) *perFileState {
	return (*perFileState)(unsafe.Pointer(pFile + uintptr(defaultSzOsFile.Load())))
}

// xOpenTrampoline is SQLite's entry point for any file open via our
// registered VFS. Forward to the default VFS, capture its installed
// io-methods, swap the visible methods to our wrapping table, and
// stash the registry token + pageSize for the io-method trampolines.
func xOpenTrampoline(tls *libc.TLS, pVfs, zName, pFile uintptr, flags int32, pOutFlags uintptr) int32 {
	defPtr := defaultVfsPtr.Load()
	def := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(defPtr))
	rc := callXOpen(tls, def.FxOpen, defPtr, zName, pFile, flags, pOutFlags)
	if rc != sqlite3.SQLITE_OK {
		return rc
	}

	base := (*sqlite3.Tsqlite3_file)(unsafe.Pointer(pFile))
	pst := perFileStateOf(pFile)
	pst.defaultMethods = base.FpMethods

	if flags&sqlite3.SQLITE_OPEN_MAIN_DB != 0 {
		ourVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(pVfs))
		fs := lookupFS(ourVfs.FpAppData)
		if fs != nil {
			pst.fsToken = fs.token
			pst.pageSize = fs.pageSize
			pst.isMain = 1
			// pst.enabled stays 0; the first xRead at offset 0 will
			// inspect the header and set it.
		}
	}

	base.FpMethods = ourIoMethodsPtr.Load()
	return sqlite3.SQLITE_OK
}

// initFromHeader inspects the first 100 bytes of a database file —
// the SQLite header — and enables/disables checksumming based on byte
// 20 (reserved_bytes). Called from xRead and xWrite when off==0 and
// the buffer covers at least the 100-byte header.
func initFromHeader(pst *perFileState, page []byte) {
	if len(page) < 100 {
		return
	}
	if !bytes.HasPrefix(page, []byte("SQLite format 3\x00")) {
		return
	}
	if page[20] == 8 {
		pst.enabled.Store(1)
	} else {
		pst.enabled.Store(0)
	}
}

func callXOpen(tls *libc.TLS, fp, pVfs, zName, pFile uintptr, flags int32, pOutFlags uintptr) int32 {
	return asFunc[func(*libc.TLS, uintptr, uintptr, uintptr, int32, uintptr) int32](fp)(tls, pVfs, zName, pFile, flags, pOutFlags)
}
