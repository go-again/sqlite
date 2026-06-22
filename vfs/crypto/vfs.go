package crypto

import (
	"errors"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"gosqlite.org/internal/cabi"
)

// perFileState lives at the tail of each Tsqlite3_file allocation
// SQLite hands to our xOpen. The wrapped VFS writes its own state into
// [0..wrappedSzOsFile-1]; we tack our state on after it, at the offset
// stored on the owning *FS. Total per-file allocation =
// fs.wrappedSzOsFile + sizeof(perFileState).
type perFileState struct {
	// defaultMethods is the saved pMethods value the wrapped VFS's
	// xOpen installed. We overwrite the file's visible pMethods with
	// our own table; this field lets io-method trampolines forward.
	defaultMethods uintptr

	// fsToken is the registry handle the trampolines use to recover
	// the owning *FS when the file is encrypted. Zero means
	// "unencrypted, forward verbatim" (auxiliary files we don't
	// touch).
	fsToken uintptr

	// pageSize is cached on the file so xRead/xWrite hot paths
	// don't need a registry lookup per call. Set by xOpen from
	// fs.pageSize.
	pageSize int32

	// fileKind tags the file as main DB / journal / WAL / temp /
	// sub-journal so the cipher can domain-separate the tweak. See
	// cipher.go's FileKind* constants. Zero means "unencrypted" and
	// pairs with fsToken == 0.
	fileKind byte

	_ [3]byte // pad to 8-byte alignment
}

// initIoMethods fills the FS's owned io-methods table. Called once at
// New() time. Each FS gets its own table because trampolines recover
// the owning *FS by reading pMethods from pFile and reverse-mapping
// via [FS.ourIoMethods]'s known offset within the FS struct. Returns
// an error on OOM so callers can fail gracefully instead of crashing
// the host process.
func (fs *FS) initIoMethods() error {
	// Allocate the methods table via libc so checkptr (-race) doesn't
	// instrument arithmetic against it — modernc's transpiled lib does
	// pointer arithmetic against the table internally.
	p := libc.Xmalloc(fs.tls, libc.Tsize_t(unsafe.Sizeof(sqlite3.Tsqlite3_io_methods{})))
	if p == 0 {
		return errors.New("crypto: alloc io-methods: out of memory")
	}
	fs.ourIoMethods = (*sqlite3.Tsqlite3_io_methods)(unsafe.Pointer(p))
	// iVersion=2 + FxShmMap non-zero is what SQLite checks before it
	// will switch to WAL journaling (see lib's _walModeCheck at the
	// FxShmMap-presence test). xShmMap/Lock/Barrier/Unmap forward
	// 1:1 to the wrapped VFS — the WAL `-shm` index file is
	// memory-mapped shared state, not user-row data, so it stays
	// plaintext on disk (see [Recorder] docstring and doc.go).
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
	return nil
}

// perFileStateOf returns the per-file state slot for pFile, using
// this FS's wrappedSzOsFile as the offset.
func (fs *FS) perFileStateOf(pFile uintptr) *perFileState {
	return (*perFileState)(unsafe.Pointer(pFile + uintptr(fs.wrappedSzOsFile)))
}

// fileMap tracks every pFile this package's xOpen handled, mapping
// it to its owning *FS. Trampolines use this map instead of
// pMethods-reverse-mapping because the latter only identifies the
// OUTERMOST VFS in a chain — an inner VFS called via the outer's
// callXFoo sees the outer's pMethods, not its own. Map keyed by pFile
// pointer is layering-agnostic. Each crypto.New() call maintains its
// own pFile entries; chained inner / outer FSes from other packages
// have their own PtrMaps and don't collide.
var fileMap = cabi.NewPtrMap[FS]()

func registerFile(pFile uintptr, fs *FS) { fileMap.Set(pFile, fs) }
func unregisterFile(pFile uintptr)       { fileMap.Delete(pFile) }
func fsForFile(pFile uintptr) *FS        { return fileMap.Get(pFile) }

// xOpenTrampoline is SQLite's entry point for any file open via our
// registered VFS. Forward to the wrapped VFS, capture its installed
// io-methods, swap the visible methods to our wrapping table, and
// stash the registry token + pageSize for the io-method trampolines.
func xOpenTrampoline(tls *libc.TLS, pVfs, zName, pFile uintptr, flags int32, pOutFlags uintptr) int32 {
	ourVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(pVfs))
	fs := lookupFS(ourVfs.FpAppData)
	if fs == nil {
		return sqlite3.SQLITE_INTERNAL
	}
	// Recipients mode resolves the cipher from the keyslot sidecar at the first
	// main-database open (before opening the file, so a failure cleans up nothing).
	// SQLite opens the main DB before any journal/WAL/temp, so the cipher is set
	// before any encrypted I/O.
	if fs.lazy && fileKindFor(flags) == FileKindMainDB {
		if err := fs.resolveCipher(libc.GoString(zName)); err != nil {
			return sqlite3.SQLITE_CANTOPEN
		}
	}
	wrappedVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(fs.wrappedVfsPtr))
	rc := cabi.CallXOpen(tls, wrappedVfs.FxOpen, fs.wrappedVfsPtr, zName, pFile, flags, pOutFlags)
	if rc != sqlite3.SQLITE_OK {
		return rc
	}

	base := (*sqlite3.Tsqlite3_file)(unsafe.Pointer(pFile))
	pst := fs.perFileStateOf(pFile)
	pst.defaultMethods = base.FpMethods

	if kind := fileKindFor(flags); kind != 0 {
		pst.fsToken = fs.token
		pst.pageSize = fs.pageSize
		pst.fileKind = kind
	}

	registerFile(pFile, fs)
	base.FpMethods = uintptr(unsafe.Pointer(fs.ourIoMethods))
	return sqlite3.SQLITE_OK
}

// fileKindFor maps an xOpen flags bitmap to the cipher's file-kind
// tag. Returns 0 (== "unencrypted, forward verbatim") for any flag
// combination we don't cover.
//
// Covered: main DB, rollback journal, WAL, temp DB, temp/sub-journal.
// The main DB is the obvious target; journal and WAL contain page
// images that would leak plaintext if left unencrypted; temp DB and
// the journals can hold spilled rows during large transactions, so
// they get the same treatment.
//
// Notable exclusion: SQLITE_OPEN_MAIN_DB's companion -shm file is
// accessed via xShmMap (a separate VFS path) rather than xRead/xWrite,
// so it doesn't pass through this trampoline at all. The shm region
// stores the WAL index — file offsets and frame pointers, not row
// data — and SQLite expects to read specific magic bytes at known
// offsets. Encrypting it would break the WAL state machine.
func fileKindFor(flags int32) byte {
	switch {
	case flags&sqlite3.SQLITE_OPEN_MAIN_DB != 0:
		return FileKindMainDB
	case flags&sqlite3.SQLITE_OPEN_MAIN_JOURNAL != 0:
		return FileKindMainJournal
	case flags&sqlite3.SQLITE_OPEN_WAL != 0:
		return FileKindWAL
	case flags&sqlite3.SQLITE_OPEN_TEMP_DB != 0:
		return FileKindTempDB
	case flags&sqlite3.SQLITE_OPEN_TEMP_JOURNAL != 0:
		return FileKindTempJournal
	case flags&sqlite3.SQLITE_OPEN_SUBJOURNAL != 0:
		return FileKindSubJournal
	default:
		return 0
	}
}
