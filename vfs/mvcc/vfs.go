package mvcc

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/go-again/sqlite/internal/cabi"
)

// fileHandles maps the (untracked-via-modernc) pFile address to its
// per-file Go-side state. The map exists because perFileState contains
// non-Go-safe pointer fields (snapshot maps, writeBuf maps) that
// cannot live in the SQLite-allocated trailing bytes of the file
// struct.
var (
	fileHandlesMu sync.RWMutex
	fileHandles   = map[uintptr]*fileHandle{}
)

// perFileState is the tail-allocated portion of the SQLite file
// struct. It only needs to hold one pointer-sized identifier — the
// Go-side fileHandle is looked up separately.
type perFileState struct {
	token uintptr // key into fileHandles
}

// fileHandle is the rich per-file state. Held in Go memory; the file
// struct's tail only stores a handle token.
type fileHandle struct {
	fs       *FS
	db       *memDB
	shared   bool
	readOnly bool

	mu       sync.Mutex
	snap     *snapshot        // captured at LOCK_SHARED
	writeBuf map[int64][]byte // private writes since RESERVED
	writeSz  int64            // file size after writes
	hasWrite bool             // true once xWrite or xTruncate has been called this txn
	lockLvl  int32            // current SQLite lock level
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
	// SQLite allocates szOsFile bytes; the modernc default VFS reserves
	// some prefix and we get the tail. szOsFile is set at registration
	// in New(); the unix sentinel is the default VFS's szOsFile field.
	// We sized our pFile alloc to default's szOsFile + perFileState
	// size; the offset is the default's szOsFile.
	//
	// But we don't store defaultSzOsFile in vfs/mvcc since we don't
	// forward to it (memdb is fully Go-side). Instead our perFileState
	// is at offset 0 — the SQLite header (Tsqlite3_file) occupies
	// FpMethods only, which is 8 bytes (a single uintptr).
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

// xOpenTrampoline allocates a perFileState + Go-side fileHandle for
// the requested DB name and rejects journal/WAL opens (in-memory MVCC
// VFS does not support persistent journals).
func xOpenTrampoline(tls *libc.TLS, pVfs, zName, pFile uintptr, flags int32, pOutFlags uintptr) int32 {
	// Refuse anything that isn't a main DB or transient: journals + WAL
	// don't have a place to live in an MVCC VFS.
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

	// Transient / temp files are always private; only main DBs get
	// shared-DB semantics via the leading-slash convention.
	shared := false
	var db *memDB
	if flags&sqlite3.SQLITE_OPEN_MAIN_DB != 0 {
		db, shared = fs.acquireDB(name)
	} else {
		db = &memDB{name: name}
		db.snap.Store(&snapshot{pages: map[int64][]byte{}})
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

// xDeleteTrampoline silently succeeds — no on-disk journals to delete.
func xDeleteTrampoline(_ *libc.TLS, _ uintptr, _ uintptr, _ int32) int32 {
	return sqlite3.SQLITE_IOERR_DELETE_NOENT
}

// xAccessTrampoline reports nothing exists.
func xAccessTrampoline(_ *libc.TLS, _ uintptr, _ uintptr, _ int32, pResOut uintptr) int32 {
	if pResOut != 0 {
		*(*int32)(unsafe.Pointer(pResOut)) = 0
	}
	return sqlite3.SQLITE_OK
}

// xFullPathnameTrampoline copies the input name through unchanged.
// SQLite expects FullPathname to canonicalize the name for use as a
// key in its lookaside cache; for in-memory storage we want the raw
// name to survive so our shared/private prefix convention works.
func xFullPathnameTrampoline(_ *libc.TLS, _ uintptr, zName uintptr, nOut int32, zOut uintptr) int32 {
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
