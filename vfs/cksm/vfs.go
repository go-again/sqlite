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
// SQLite hands to our xOpen. The wrapped VFS writes its own state into
// [0..wrappedSzOsFile-1]; we tack our state on after it, at the offset
// stored on the owning *FS.
type perFileState struct {
	defaultMethods uintptr // pMethods value the wrapped VFS installed
	fsToken        uintptr // registry handle; 0 = forward verbatim
	pageSize       int32
	// enabled is 1 once the file's SQLite header has been inspected
	// and reserved_bytes==8 confirmed; 0 means pass-through.
	// atomic.Int32 because xRead and xWrite can race across different
	// pFiles for the same FS.
	enabled atomic.Int32
	isMain  int32   // 1 for main DB, 0 for everything else
	_       [4]byte // pad to 8-byte alignment
}

// initIoMethods fills the FS's owned io-methods table. The table is
// allocated via libc so checkptr (-race) doesn't track its
// arithmetic — modernc's transpiled lib does pointer-arithmetic
// against the methods struct internally, and Go-heap allocations
// would trip the analyzer.
func (fs *FS) initIoMethods() {
	p := libc.Xmalloc(fs.tls, libc.Tsize_t(unsafe.Sizeof(sqlite3.Tsqlite3_io_methods{})))
	if p == 0 {
		panic("cksm: alloc io-methods: out of memory")
	}
	fs.ourIoMethods = (*sqlite3.Tsqlite3_io_methods)(unsafe.Pointer(p))
	*fs.ourIoMethods = sqlite3.Tsqlite3_io_methods{
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
}

func (fs *FS) perFileStateOf(pFile uintptr) *perFileState {
	return (*perFileState)(unsafe.Pointer(pFile + uintptr(fs.wrappedSzOsFile)))
}

// fileMap maps every pFile this package's xOpen handled to its
// owning *FS. Trampolines look up by pFile pointer so chained
// inner/outer VFSes (e.g. crypto-on-cksm) don't collide — each
// package's file map is independent.
var fileMap struct {
	mu sync.RWMutex
	m  map[uintptr]*FS
}

func init() { fileMap.m = make(map[uintptr]*FS) }

func registerFile(pFile uintptr, fs *FS) {
	fileMap.mu.Lock()
	fileMap.m[pFile] = fs
	fileMap.mu.Unlock()
}

func unregisterFile(pFile uintptr) {
	fileMap.mu.Lock()
	delete(fileMap.m, pFile)
	fileMap.mu.Unlock()
}

func fsForFile(pFile uintptr) *FS {
	fileMap.mu.RLock()
	fs := fileMap.m[pFile]
	fileMap.mu.RUnlock()
	return fs
}

// xOpenTrampoline forwards to the wrapped VFS, captures its methods,
// and installs our own.
func xOpenTrampoline(tls *libc.TLS, pVfs, zName, pFile uintptr, flags int32, pOutFlags uintptr) int32 {
	ourVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(pVfs))
	fs := lookupFS(ourVfs.FpAppData)
	if fs == nil {
		return sqlite3.SQLITE_INTERNAL
	}
	wrappedVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(fs.wrappedVfsPtr))
	rc := cabi.CallXOpen(tls, wrappedVfs.FxOpen, fs.wrappedVfsPtr, zName, pFile, flags, pOutFlags)
	if rc != sqlite3.SQLITE_OK {
		return rc
	}

	base := (*sqlite3.Tsqlite3_file)(unsafe.Pointer(pFile))
	pst := fs.perFileStateOf(pFile)
	pst.defaultMethods = base.FpMethods

	if flags&sqlite3.SQLITE_OPEN_MAIN_DB != 0 {
		pst.fsToken = fs.token
		pst.pageSize = fs.pageSize
		pst.isMain = 1
		// pst.enabled stays 0; the first xRead at offset 0 will
		// inspect the header and set it.
	}

	registerFile(pFile, fs)
	base.FpMethods = uintptr(unsafe.Pointer(fs.ourIoMethods))
	return sqlite3.SQLITE_OK
}

// initFromHeader inspects the first 100 bytes of a database file —
// the SQLite header — and enables/disables checksumming based on byte
// 20 (reserved_bytes).
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
