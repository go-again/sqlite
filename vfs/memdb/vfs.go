package memdb

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/go-again/sqlite/internal/cabi"
)

var (
	fileHandlesMu sync.RWMutex
	fileHandles   = map[uintptr]*fileHandle{}
)

type perFileState struct {
	token uintptr
}

type fileHandle struct {
	fs       *FS
	db       *memDB
	shared   bool
	readOnly bool
}

var initOnce sync.Once

var (
	ourIoMethodsPtr atomic.Uintptr
	ourIoMethods    sqlite3.Tsqlite3_io_methods
)

func initFromDefault(_ *sqlite3.Tsqlite3_vfs) {
	ourIoMethods = sqlite3.Tsqlite3_io_methods{
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
	ourIoMethodsPtr.Store(uintptr(unsafe.Pointer(&ourIoMethods)))
}

func perFileStateOf(pFile uintptr) *perFileState {
	return (*perFileState)(unsafe.Pointer(pFile + unsafe.Sizeof(sqlite3.Tsqlite3_file{})))
}

func handleFor(pFile uintptr) *fileHandle {
	tok := perFileStateOf(pFile).token
	fileHandlesMu.RLock()
	h := fileHandles[tok]
	fileHandlesMu.RUnlock()
	return h
}

func storeHandle(h *fileHandle) uintptr {
	tok := uintptr(nextToken.Add(1))
	fileHandlesMu.Lock()
	fileHandles[tok] = h
	fileHandlesMu.Unlock()
	return tok
}

func dropHandle(tok uintptr) {
	fileHandlesMu.Lock()
	delete(fileHandles, tok)
	fileHandlesMu.Unlock()
}

func xOpenTrampoline(tls *libc.TLS, pVfs, zName, pFile uintptr, flags int32, pOutFlags uintptr) int32 {
	const databases = sqlite3.SQLITE_OPEN_MAIN_DB | sqlite3.SQLITE_OPEN_TEMP_DB | sqlite3.SQLITE_OPEN_TRANSIENT_DB
	if flags&databases == 0 && flags&sqlite3.SQLITE_OPEN_DELETEONCLOSE == 0 {
		return sqlite3.SQLITE_CANTOPEN
	}

	name := ""
	if zName != 0 {
		name = libc.GoString(zName)
	}

	ourVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(pVfs))
	fs := lookupFS(ourVfs.FpAppData)
	if fs == nil {
		return sqlite3.SQLITE_INTERNAL
	}

	shared := false
	var db *memDB
	if flags&sqlite3.SQLITE_OPEN_MAIN_DB != 0 {
		db, shared = fs.acquireDB(name)
	} else {
		db = &memDB{name: name, pages: map[int64][]byte{}}
		db.refs.Add(1)
	}

	h := &fileHandle{
		fs:       fs,
		db:       db,
		shared:   shared,
		readOnly: flags&sqlite3.SQLITE_OPEN_READONLY != 0,
	}
	pst := perFileStateOf(pFile)
	pst.token = storeHandle(h)

	base := (*sqlite3.Tsqlite3_file)(unsafe.Pointer(pFile))
	base.FpMethods = ourIoMethodsPtr.Load()

	if pOutFlags != 0 {
		*(*int32)(unsafe.Pointer(pOutFlags)) = flags | sqlite3.SQLITE_OPEN_MEMORY
	}
	return sqlite3.SQLITE_OK
}

func xDeleteTrampoline(_ *libc.TLS, _, _ uintptr, _ int32) int32 {
	return sqlite3.SQLITE_IOERR_DELETE_NOENT
}

func xAccessTrampoline(_ *libc.TLS, _, _ uintptr, _ int32, pResOut uintptr) int32 {
	if pResOut != 0 {
		*(*int32)(unsafe.Pointer(pResOut)) = 0
	}
	return sqlite3.SQLITE_OK
}

func xFullPathnameTrampoline(_ *libc.TLS, _, zName uintptr, nOut int32, zOut uintptr) int32 {
	n := 0
	for n < int(nOut)-1 {
		b := *(*byte)(unsafe.Pointer(zName + uintptr(n)))
		if b == 0 {
			break
		}
		*(*byte)(unsafe.Pointer(zOut + uintptr(n))) = b
		n++
	}
	*(*byte)(unsafe.Pointer(zOut + uintptr(n))) = 0
	return sqlite3.SQLITE_OK
}
