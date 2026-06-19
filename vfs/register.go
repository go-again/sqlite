package vfs

import (
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"gosqlite.org/internal/cabi"
)

// registeredVFS is the Go-side state behind one [Register]ed name. The
// token in its FpAppData slot recovers it inside every VFS-method
// trampoline; the C VFS struct (cvfs) and name (cname) are owned here
// and freed on Unregister. tls is a dedicated libc thread-local the
// alloc/register calls run on, matching every other VFS sub-package.
type registeredVFS struct {
	impl  VFS
	name  string
	cname uintptr
	cvfs  uintptr
	tls   *libc.TLS
	token uintptr
}

// openFile is the per-open state recovered from a file's tail
// allocation. It remembers the open flags and name so xClose can honour
// OpenDeleteOnClose without a second lookup. shm/shmKey are populated
// lazily on the first xShmMap when the File implements [ShmFile].
type openFile struct {
	file   File
	vfs    *registeredVFS
	name   string
	flags  OpenFlags
	shm    *shmGroup
	shmKey string
}

// perFileState is the tail block appended after each Tsqlite3_file: it
// holds the token that recovers the openFile from fileRegistry.
type perFileState struct {
	token uintptr
}

var (
	// vfsRegistry maps pVfs->FpAppData → *registeredVFS.
	vfsRegistry = cabi.NewRegistry[registeredVFS]()
	// fileRegistry maps a file's tail token → *openFile.
	fileRegistry = cabi.NewRegistry[openFile]()

	// stateMu guards byName and serialises register/unregister so the C
	// VFS list and the Go bookkeeping never diverge.
	stateMu sync.Mutex
	byName  = map[string]*registeredVFS{}
)

func perFileStateOf(pFile uintptr) *perFileState {
	return (*perFileState)(unsafe.Pointer(pFile + unsafe.Sizeof(sqlite3.Tsqlite3_file{})))
}

func fileFor(pFile uintptr) *openFile {
	return fileRegistry.Lookup(perFileStateOf(pFile).token)
}

func vfsFor(pVfs uintptr) *registeredVFS {
	return vfsRegistry.Lookup((*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(pVfs)).FpAppData)
}

// Register installs impl as a named SQLite VFS. Open a database against
// it with `?vfs=<name>` in the DSN (or sqlite.Config.VFS). The name
// must be unique and non-empty. Call [Unregister] to remove it.
//
// Register is safe for concurrent use; each call installs an
// independent VFS. impl may back many open databases at once, so it
// must be safe for concurrent Open calls (see [VFS]).
func Register(name string, impl VFS, opts ...Option) error {
	if name == "" {
		return fmt.Errorf("vfs: Register: empty name")
	}
	if impl == nil {
		return fmt.Errorf("vfs: Register: nil VFS")
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	stateMu.Lock()
	defer stateMu.Unlock()
	if _, dup := byName[name]; dup {
		return fmt.Errorf("vfs: Register: %q already registered", name)
	}

	tls := libc.NewTLS()
	defPtr := sqlite3.Xsqlite3_vfs_find(tls, 0)
	if defPtr == 0 {
		tls.Close()
		return fmt.Errorf("vfs: Register: no default VFS")
	}
	defVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(defPtr))
	initOnce.Do(func() {
		initIoMethods()
		initShmIoMethods()
	})

	cname, err := libc.CString(name)
	if err != nil {
		tls.Close()
		return fmt.Errorf("vfs: Register: alloc name: %w", err)
	}
	cvfs := libc.Xmalloc(tls, libc.Tsize_t(unsafe.Sizeof(sqlite3.Tsqlite3_vfs{})))
	if cvfs == 0 {
		libc.Xfree(tls, cname)
		tls.Close()
		return fmt.Errorf("vfs: Register: alloc VFS struct: out of memory")
	}

	rv := &registeredVFS{impl: impl, name: name, cname: cname, cvfs: cvfs, tls: tls}
	rv.token = vfsRegistry.Register(rv)

	mxPath := defVfs.FmxPathname
	if o.maxPathname > 0 {
		mxPath = int32(o.maxPathname)
	}
	// Tail = base SQLite file struct + our perFileState token. We never
	// forward I/O to the default VFS, so the default's private tail is
	// not needed.
	szOsFile := int32(unsafe.Sizeof(sqlite3.Tsqlite3_file{}) + unsafe.Sizeof(perFileState{}))

	// Field-by-field (never memcpy) so a modernc bump that reorders or
	// removes a field fails to compile rather than scrambling layout
	// (CLAUDE.md §3). The clock/sleep/randomness/last-error methods
	// delegate to the platform VFS — a custom backend should not have to
	// reimplement wall-clock time or the PRNG.
	*(*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(cvfs)) = sqlite3.Tsqlite3_vfs{
		FiVersion:          1,
		FszOsFile:          szOsFile,
		FmxPathname:        mxPath,
		FpNext:             0,
		FzName:             cname,
		FpAppData:          rv.token,
		FxOpen:             cabi.FuncPointer(xOpenTrampoline),
		FxDelete:           cabi.FuncPointer(xDeleteTrampoline),
		FxAccess:           cabi.FuncPointer(xAccessTrampoline),
		FxFullPathname:     cabi.FuncPointer(xFullPathnameTrampoline),
		FxRandomness:       defVfs.FxRandomness,
		FxSleep:            defVfs.FxSleep,
		FxCurrentTime:      defVfs.FxCurrentTime,
		FxGetLastError:     defVfs.FxGetLastError,
		FxCurrentTimeInt64: defVfs.FxCurrentTimeInt64,
	}

	makeDefault := int32(0)
	if o.makeDefault {
		makeDefault = 1
	}
	if rc := sqlite3.Xsqlite3_vfs_register(tls, cvfs, makeDefault); rc != sqlite3.SQLITE_OK {
		vfsRegistry.Unregister(rv.token)
		libc.Xfree(tls, cvfs)
		libc.Xfree(tls, cname)
		tls.Close()
		return fmt.Errorf("vfs: Register: vfs_register rc=%d", rc)
	}
	byName[name] = rv
	return nil
}

// Unregister removes a VFS previously installed by [Register]. It fails
// if any database is still open against the VFS — closing those first
// is the caller's responsibility, and leaving them open would dangle
// the C VFS struct the open files still reference.
func Unregister(name string) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	rv := byName[name]
	if rv == nil {
		return fmt.Errorf("vfs: Unregister: %q not registered", name)
	}

	open := false
	fileRegistry.Range(func(_ uintptr, of *openFile) bool {
		if of.vfs == rv {
			open = true
			return false
		}
		return true
	})
	if open {
		return fmt.Errorf("vfs: Unregister: %q has open files", name)
	}

	rc := sqlite3.Xsqlite3_vfs_unregister(rv.tls, rv.cvfs)
	vfsRegistry.Unregister(rv.token)
	libc.Xfree(rv.tls, rv.cvfs)
	libc.Xfree(rv.tls, rv.cname)
	rv.tls.Close()
	delete(byName, name)
	if rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("vfs: Unregister: vfs_unregister rc=%d", rc)
	}
	return nil
}

// Find returns the [VFS] registered under name, or (nil, false).
func Find(name string) (VFS, bool) {
	stateMu.Lock()
	defer stateMu.Unlock()
	if rv := byName[name]; rv != nil {
		return rv.impl, true
	}
	return nil, false
}

// --- VFS-method trampolines (fire from transpiled C) ---

func xOpenTrampoline(_ *libc.TLS, pVfs, zName, pFile uintptr, flags int32, pOutFlags uintptr) int32 {
	rv := vfsFor(pVfs)
	if rv == nil {
		return sqlite3.SQLITE_INTERNAL
	}
	name := ""
	if zName != 0 {
		name = libc.GoString(zName)
	}

	f, granted, err := rv.impl.Open(name, OpenFlags(flags))
	if err != nil {
		return codeOf(err, sqlite3.SQLITE_CANTOPEN)
	}
	if f == nil {
		return sqlite3.SQLITE_CANTOPEN
	}
	if granted == 0 {
		granted = OpenFlags(flags)
	}

	of := &openFile{file: f, vfs: rv, name: name, flags: OpenFlags(flags)}
	perFileStateOf(pFile).token = fileRegistry.Register(of)
	// A File that implements ShmFile gets the iVersion-2 methods table
	// (with the xShm* slots) so SQLite will offer it WAL mode; everyone
	// else gets the iVersion-1 table and stays in rollback-journal mode.
	methods := ioMethodsPtr.Load()
	if _, ok := f.(ShmFile); ok {
		methods = ioMethodsShmPtr.Load()
	}
	(*sqlite3.Tsqlite3_file)(unsafe.Pointer(pFile)).FpMethods = methods

	if pOutFlags != 0 {
		*(*int32)(unsafe.Pointer(pOutFlags)) = int32(granted)
	}
	return sqlite3.SQLITE_OK
}

func xDeleteTrampoline(_ *libc.TLS, pVfs, zName uintptr, syncDir int32) int32 {
	rv := vfsFor(pVfs)
	if rv == nil {
		return sqlite3.SQLITE_INTERNAL
	}
	err := rv.impl.Delete(libc.GoString(zName), syncDir != 0)
	if err != nil {
		if isNotExist(err) {
			return sqlite3.SQLITE_IOERR_DELETE_NOENT
		}
		return codeOf(err, sqlite3.SQLITE_IOERR_DELETE)
	}
	return sqlite3.SQLITE_OK
}

func xAccessTrampoline(_ *libc.TLS, pVfs, zName uintptr, op int32, pResOut uintptr) int32 {
	rv := vfsFor(pVfs)
	if rv == nil {
		return sqlite3.SQLITE_INTERNAL
	}
	ok, err := rv.impl.Access(libc.GoString(zName), AccessOp(op))
	res := int32(0)
	if err == nil && ok {
		res = 1
	}
	if pResOut != 0 {
		*(*int32)(unsafe.Pointer(pResOut)) = res
	}
	if err != nil && !isNotExist(err) {
		return codeOf(err, sqlite3.SQLITE_IOERR_ACCESS)
	}
	return sqlite3.SQLITE_OK
}

func xFullPathnameTrampoline(_ *libc.TLS, pVfs, zName uintptr, nOut int32, zOut uintptr) int32 {
	rv := vfsFor(pVfs)
	if rv == nil {
		return sqlite3.SQLITE_INTERNAL
	}
	full, err := rv.impl.FullPathname(libc.GoString(zName))
	if err != nil {
		return codeOf(err, sqlite3.SQLITE_CANTOPEN)
	}
	b := []byte(full)
	if len(b)+1 > int(nOut) {
		return sqlite3.SQLITE_CANTOPEN
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(zOut)), int(nOut))
	copy(dst, b)
	dst[len(b)] = 0
	return sqlite3.SQLITE_OK
}

var initOnce sync.Once

var (
	ioMethods    sqlite3.Tsqlite3_io_methods
	ioMethodsPtr atomic.Uintptr
)
