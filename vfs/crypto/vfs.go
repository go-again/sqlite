package crypto

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// perFileState lives at the tail of each Tsqlite3_file allocation
// SQLite hands to our xOpen. The default unix VFS writes its own
// TunixFile into [0..defaultSzOsFile-1]; we tack our state on after
// it. Tsqlite3_vfs.FszOsFile is the sum of both at registration.
type perFileState struct {
	// defaultMethods is the saved pMethods value the default VFS's
	// xOpen installed. We overwrite the file's visible pMethods with
	// our own table; this field lets io-method trampolines forward.
	defaultMethods uintptr

	// fsToken is the registry handle the trampolines use to recover
	// the owning *FS (and thus cipher + pageSize). Set by xOpen.
	// Zero means "this file isn't encrypted" — we forward verbatim
	// in that case (used for the WAL `-shm` index, which has no
	// xRead/xWrite path, plus any open flag fileKindFor returns 0 for).
	fsToken uintptr

	// pageSize is cached on the file so xRead/xWrite hot paths
	// don't need a map lookup per call. Set by xOpen from fs.pageSize.
	pageSize int32

	// fileKind tags the file as main DB / journal / WAL / temp /
	// sub-journal so the cipher can domain-separate the tweak. See
	// cipher.go's fileKind* constants. Zero means "unencrypted" and
	// pairs with fsToken == 0.
	fileKind byte

	_ [3]byte // pad to 8-byte alignment
}

// initOnce guards package-level VFS state populated from the default
// VFS the first time New runs.
var initOnce sync.Once

// Package-level state populated once via initOnce. Reads after init
// happen on every xOpen / io-method trampoline; they're wrapped in
// atomics so the Go memory model formally publishes the writes. The
// values never change after the first New(): the default VFS is
// process-global, the singleton io-methods table is address-stable
// (package-level var), and the per-file offset depends only on the
// default VFS's szOsFile.
var (
	defaultSzOsFile atomic.Int32   // size of the default VFS's per-file allocation
	defaultVfsPtr   atomic.Uintptr // pointer to the default Tsqlite3_vfs (for xOpen forwarding)
	ourIoMethodsPtr atomic.Uintptr // pointer to our singleton io-methods table

	// ourIoMethods is the methods table itself. Address-stable
	// (package-level var); ourIoMethodsPtr stores its address.
	ourIoMethods sqlite3.Tsqlite3_io_methods
)

func initFromDefault(def *sqlite3.Tsqlite3_vfs) {
	defaultSzOsFile.Store(def.FszOsFile)
	defaultVfsPtr.Store(uintptr(unsafe.Pointer(def)))
	ourIoMethods = sqlite3.Tsqlite3_io_methods{
		FiVersion:               1,
		FxClose:                 cFuncPointer(xCloseTrampoline),
		FxRead:                  cFuncPointer(xReadTrampoline),
		FxWrite:                 cFuncPointer(xWriteTrampoline),
		FxTruncate:              cFuncPointer(xTruncateTrampoline),
		FxSync:                  cFuncPointer(xSyncTrampoline),
		FxFileSize:              cFuncPointer(xFileSizeTrampoline),
		FxLock:                  cFuncPointer(xLockTrampoline),
		FxUnlock:                cFuncPointer(xUnlockTrampoline),
		FxCheckReservedLock:     cFuncPointer(xCheckReservedLockTrampoline),
		FxFileControl:           cFuncPointer(xFileControlTrampoline),
		FxSectorSize:            cFuncPointer(xSectorSizeTrampoline),
		FxDeviceCharacteristics: cFuncPointer(xDeviceCharacteristicsTrampoline),
	}
	ourIoMethodsPtr.Store(uintptr(unsafe.Pointer(&ourIoMethods)))
}

func perFileStateOf(pFile uintptr) *perFileState {
	return (*perFileState)(unsafe.Pointer(pFile + uintptr(defaultSzOsFile.Load())))
}

// fsFor returns the *FS responsible for the given file, or nil if
// the file is unencrypted (an auxiliary file we forward verbatim).
func fsFor(pFile uintptr) *FS {
	tok := perFileStateOf(pFile).fsToken
	if tok == 0 {
		return nil
	}
	return lookupFS(tok)
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

	if kind := fileKindFor(flags); kind != 0 {
		ourVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(pVfs))
		fs := lookupFS(ourVfs.FpAppData)
		if fs != nil {
			pst.fsToken = fs.token
			pst.pageSize = fs.pageSize
			pst.fileKind = kind
		}
	}

	base.FpMethods = ourIoMethodsPtr.Load()
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
		return fileKindMainDB
	case flags&sqlite3.SQLITE_OPEN_MAIN_JOURNAL != 0:
		return fileKindMainJournal
	case flags&sqlite3.SQLITE_OPEN_WAL != 0:
		return fileKindWAL
	case flags&sqlite3.SQLITE_OPEN_TEMP_DB != 0:
		return fileKindTempDB
	case flags&sqlite3.SQLITE_OPEN_TEMP_JOURNAL != 0:
		return fileKindTempJournal
	case flags&sqlite3.SQLITE_OPEN_SUBJOURNAL != 0:
		return fileKindSubJournal
	default:
		return 0
	}
}

func callXOpen(tls *libc.TLS, fp, pVfs, zName, pFile uintptr, flags int32, pOutFlags uintptr) int32 {
	return (*(*func(*libc.TLS, uintptr, uintptr, uintptr, int32, uintptr) int32)(
		unsafe.Pointer(&struct{ uintptr }{fp})))(tls, pVfs, zName, pFile, flags, pOutFlags)
}
