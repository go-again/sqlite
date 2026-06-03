package cksm

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/go-again/sqlite/internal/cabi"
)

// Options configures a [New] registration. All fields are optional.
type Options struct {
	// PageSize must match the database's PRAGMA page_size. Defaults to
	// 4096 (SQLite's default). Must be a power of two in [512, 65536].
	PageSize int

	// WrapVFS is the name of an existing registered VFS that this
	// cksm VFS should layer on top of. Empty means wrap the system
	// default VFS. Set to a [vfs/crypto] name to get
	// "checksummed-then-encrypted" composition.
	WrapVFS string
}

// FS is a registered checksum VFS. Each [New] returns a distinct FS;
// call Close to unregister and release resources.
type FS struct {
	name     string
	cname    uintptr
	cvfs     uintptr
	tls      *libc.TLS
	pageSize int32
	token    uintptr
	closed   atomic.Bool

	// ourIoMethods lives in its own heap allocation rather than as an
	// inline FS field. Two reasons: (1) it has a stable address that
	// can be safely cast to uintptr for the C side, and (2) Go's
	// checkptr (-race) is happier with a Tsqlite3_io_methods-sized
	// allocation than a field inside a larger struct when modernc's
	// transpiled lib does pointer arithmetic against the methods
	// table.
	ourIoMethods *sqlite3.Tsqlite3_io_methods

	// wrappedVfsPtr is the Tsqlite3_vfs we forward xOpen to. With
	// Options.WrapVFS == "" this is the system default VFS; otherwise
	// it is the VFS whose name the caller passed.
	wrappedVfsPtr uintptr
	// wrappedSzOsFile is the wrapped VFS's szOsFile. Our perFileState
	// lives at this offset from pFile.
	wrappedSzOsFile int32
}

const defaultPageSize = 4096

var newVfsID atomic.Uint64

// stateMu serializes [New] and [FS.Close] so libc TLS setup and the
// global VFS registration table aren't raced.
var stateMu sync.Mutex

// fsRegistry maps the opaque token stored in pVfs->FpAppData (and
// later copied into per-file state) back to the *FS that owns it.
// Through a registry rather than raw unsafe.Pointer so the FS stays
// GC-reachable while C-side state holds a reference.
var (
	fsRegistryMu sync.RWMutex
	fsRegistry   = map[uintptr]*FS{}
	nextFSToken  atomic.Uint64
)

func registerFS(fs *FS) uintptr {
	tok := uintptr(nextFSToken.Add(1))
	fsRegistryMu.Lock()
	fsRegistry[tok] = fs
	fsRegistryMu.Unlock()
	return tok
}

func lookupFS(tok uintptr) *FS {
	fsRegistryMu.RLock()
	defer fsRegistryMu.RUnlock()
	return fsRegistry[tok]
}

func unregisterFS(tok uintptr) {
	fsRegistryMu.Lock()
	delete(fsRegistry, tok)
	fsRegistryMu.Unlock()
}

// New registers a checksum VFS and returns its name (slot into a DSN
// as `?vfs=<name>`), a handle for cleanup, and any error.
//
// Each call registers a distinct VFS. Calls from multiple goroutines
// are safe — a package-level mutex serializes them.
func New(opts Options) (name string, fs *FS, err error) {
	pageSize := opts.PageSize
	if pageSize == 0 {
		pageSize = defaultPageSize
	}
	if !isValidPageSize(pageSize) {
		return "", nil, fmt.Errorf("cksm: invalid PageSize %d (must be power of two in [512, 65536])", pageSize)
	}

	stateMu.Lock()
	defer stateMu.Unlock()

	tls := libc.NewTLS()
	var defPtr uintptr
	if opts.WrapVFS != "" {
		cName, allocErr := libc.CString(opts.WrapVFS)
		if allocErr != nil {
			tls.Close()
			return "", nil, fmt.Errorf("cksm: alloc WrapVFS name: %w", allocErr)
		}
		defPtr = sqlite3.Xsqlite3_vfs_find(tls, cName)
		libc.Xfree(tls, cName)
		if defPtr == 0 {
			tls.Close()
			return "", nil, fmt.Errorf("cksm: WrapVFS %q not registered", opts.WrapVFS)
		}
	} else {
		defPtr = sqlite3.Xsqlite3_vfs_find(tls, 0)
		if defPtr == 0 {
			tls.Close()
			return "", nil, fmt.Errorf("cksm: Xsqlite3_vfs_find(NULL) returned 0; no default VFS registered")
		}
	}
	defVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(defPtr))

	name = fmt.Sprintf("cksm%x", newVfsID.Add(1))
	cname, allocErr := libc.CString(name)
	if allocErr != nil {
		tls.Close()
		return "", nil, fmt.Errorf("cksm: alloc VFS name: %w", allocErr)
	}

	cvfs := libc.Xmalloc(tls, libc.Tsize_t(unsafe.Sizeof(sqlite3.Tsqlite3_vfs{})))
	if cvfs == 0 {
		libc.Xfree(tls, cname)
		tls.Close()
		return "", nil, fmt.Errorf("cksm: alloc VFS struct: out of memory")
	}

	fs = &FS{
		name:            name,
		cname:           cname,
		cvfs:            cvfs,
		tls:             tls,
		pageSize:        int32(pageSize),
		wrappedVfsPtr:   defPtr,
		wrappedSzOsFile: defVfs.FszOsFile,
	}
	fs.initIoMethods()
	fs.token = registerFS(fs)

	ourVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(cvfs))
	*ourVfs = sqlite3.Tsqlite3_vfs{
		FiVersion:          1,
		FszOsFile:          defVfs.FszOsFile + int32(unsafe.Sizeof(perFileState{})),
		FmxPathname:        defVfs.FmxPathname,
		FpNext:             0,
		FzName:             cname,
		FpAppData:          fs.token,
		FxOpen:             cabi.FuncPointer(xOpenTrampoline),
		FxDelete:           defVfs.FxDelete,
		FxAccess:           defVfs.FxAccess,
		FxFullPathname:     defVfs.FxFullPathname,
		FxDlOpen:           defVfs.FxDlOpen,
		FxDlError:          defVfs.FxDlError,
		FxDlSym:            defVfs.FxDlSym,
		FxDlClose:          defVfs.FxDlClose,
		FxRandomness:       defVfs.FxRandomness,
		FxSleep:            defVfs.FxSleep,
		FxCurrentTime:      defVfs.FxCurrentTime,
		FxGetLastError:     defVfs.FxGetLastError,
		FxCurrentTimeInt64: defVfs.FxCurrentTimeInt64,
		FxSetSystemCall:    defVfs.FxSetSystemCall,
		FxGetSystemCall:    defVfs.FxGetSystemCall,
		FxNextSystemCall:   defVfs.FxNextSystemCall,
	}

	if rc := sqlite3.Xsqlite3_vfs_register(tls, cvfs, 0); rc != sqlite3.SQLITE_OK {
		unregisterFS(fs.token)
		libc.Xfree(tls, cvfs)
		libc.Xfree(tls, cname)
		tls.Close()
		return "", nil, fmt.Errorf("cksm: Xsqlite3_vfs_register rc=%d", rc)
	}
	return name, fs, nil
}

// Name returns the registered VFS name.
func (f *FS) Name() string { return f.name }

// Close unregisters the VFS and frees its libc allocations. Idempotent.
func (f *FS) Close() error {
	if f.closed.Swap(true) {
		return nil
	}
	stateMu.Lock()
	defer stateMu.Unlock()

	rc := sqlite3.Xsqlite3_vfs_unregister(f.tls, f.cvfs)
	unregisterFS(f.token)
	libc.Xfree(f.tls, f.cvfs)
	libc.Xfree(f.tls, f.cname)
	if f.ourIoMethods != nil {
		libc.Xfree(f.tls, uintptr(unsafe.Pointer(f.ourIoMethods)))
		f.ourIoMethods = nil
	}
	f.tls.Close()
	if rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("cksm: Xsqlite3_vfs_unregister rc=%d", rc)
	}
	return nil
}

func isValidPageSize(n int) bool {
	if n < 512 || n > 65536 {
		return false
	}
	return n&(n-1) == 0
}

// Enabler is the subset of *sqlite.Conn that [EnableChecksums]
// needs. Any type with the same shape can satisfy it — typically the
// root package's *sqlite.Conn.
type Enabler interface {
	EnableChecksums(schema string) error
}

// EnableChecksums is a thin wrapper around the root package's
// (*sqlite.Conn).EnableChecksums method, kept for backwards
// compatibility with older callers. New code should call the method
// directly on the connection.
//
// schema is the attached-database name ("main" for the primary
// database; "temp" or a name from ATTACH DATABASE otherwise). Pass
// "main" if you only have the default database.
//
// Mirrors the upstream cksum-vfs convention; see
// https://sqlite.org/cksumvfs.html.
func EnableChecksums(c Enabler, schema string) error {
	return c.EnableChecksums(schema)
}

// compute is the SQLite cksm_vtab Fletcher-style rolling 64-bit
// checksum: two interleaved 32-bit sums over the page's 8-byte
// little-endian words. Identical algorithm to the upstream cksm_vtab
// extension; on-disk compatible.
func compute(a []byte) (cksm [8]byte) {
	var s1, s2 uint32
	for len(a) >= 8 {
		s1 += binary.LittleEndian.Uint32(a[0:4]) + s2
		s2 += binary.LittleEndian.Uint32(a[4:8]) + s1
		a = a[8:]
	}
	binary.LittleEndian.PutUint32(cksm[0:4], s1)
	binary.LittleEndian.PutUint32(cksm[4:8], s2)
	return cksm
}
