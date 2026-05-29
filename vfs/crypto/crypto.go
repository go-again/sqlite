package crypto

import (
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// Cipher selects the encryption mode.
type Cipher int

const (
	// Adiantum is the default. 32-byte key, length-preserving
	// tweakable wide-block cipher (XChaCha12 + Poly1305 + NH + AES).
	// Pure Go via lukechampine.com/adiantum.
	Adiantum Cipher = iota
	// AESXTS is AES-XTS-256. 64-byte key = two AES-256 keys.
	// Backed by golang.org/x/crypto/xts. For compliance regimes
	// that mandate AES.
	AESXTS
)

// Options configures [New].
type Options struct {
	// Key is the raw cipher key. Length depends on Cipher: 32 bytes
	// for Adiantum, 64 bytes for AES-XTS-256.
	Key []byte

	// Cipher picks the mode. Defaults to Adiantum.
	Cipher Cipher

	// PageSize must match the database's PRAGMA page_size. Defaults
	// to 4096 (SQLite's default). Set explicitly when the DB was
	// created with a non-default page size. Must be a power of two
	// between 512 and 65536 inclusive.
	PageSize int
}

// FS is a registered encryption VFS. Each [New] returns a distinct
// FS; call Close to unregister and release resources.
type FS struct {
	name     string
	cname    uintptr // libc-allocated C-string copy of name
	cvfs     uintptr // libc-allocated Tsqlite3_vfs we registered
	tls      *libc.TLS
	cipher   pageCipher
	pageSize int32
	token    uintptr // registry handle, stored in pVfs->FpAppData
	closed   atomic.Bool
}

const defaultPageSize = 4096

// newVfsID supplies the suffix for the registered VFS name. Multiple
// [New] calls produce distinct VFSes; SQLite enforces unique names.
var newVfsID atomic.Uint64

// stateMu serializes [New] and [FS.Close] so libc TLS setup and the
// global VFS registration table aren't raced.
var stateMu sync.Mutex

// fsRegistry maps the opaque token stored in pVfs->FpAppData (and
// later copied into per-file state) back to the *FS that owns the
// cipher. Goes through a registry rather than raw unsafe.Pointer so
// the FS stays GC-reachable while C-side state holds a reference.
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

// New registers an encryption VFS and returns its name (slot into a
// DSN as `?vfs=<name>`), a handle for cleanup, and any error.
//
// Each call registers a distinct VFS — safe to call concurrently.
// Adiantum is the only mode currently wired; passing [AESXTS]
// returns an error until Phase 3 lands.
func New(opts Options) (name string, fs *FS, err error) {
	pageSize := opts.PageSize
	if pageSize == 0 {
		pageSize = defaultPageSize
	}
	if !isValidPageSize(pageSize) {
		return "", nil, fmt.Errorf("crypto: invalid PageSize %d (must be power of two in [512, 65536])", pageSize)
	}
	cipher, err := newCipher(opts.Cipher, opts.Key)
	if err != nil {
		return "", nil, err
	}

	stateMu.Lock()
	defer stateMu.Unlock()

	tls := libc.NewTLS()
	defaultVfsPtr := sqlite3.Xsqlite3_vfs_find(tls, 0)
	if defaultVfsPtr == 0 {
		tls.Close()
		return "", nil, fmt.Errorf("crypto: Xsqlite3_vfs_find(NULL) returned 0; no default VFS registered")
	}
	defaultVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(defaultVfsPtr))
	initOnce.Do(func() { initFromDefault(defaultVfs) })

	name = fmt.Sprintf("crypto%x", newVfsID.Add(1))
	cname, allocErr := libc.CString(name)
	if allocErr != nil {
		tls.Close()
		return "", nil, fmt.Errorf("crypto: alloc VFS name: %w", allocErr)
	}

	cvfs := libc.Xmalloc(tls, libc.Tsize_t(unsafe.Sizeof(sqlite3.Tsqlite3_vfs{})))
	if cvfs == 0 {
		libc.Xfree(tls, cname)
		tls.Close()
		return "", nil, fmt.Errorf("crypto: alloc VFS struct: out of memory")
	}

	fs = &FS{
		name:     name,
		cname:    cname,
		cvfs:     cvfs,
		tls:      tls,
		cipher:   cipher,
		pageSize: int32(pageSize),
	}
	fs.token = registerFS(fs)

	ourVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(cvfs))
	*ourVfs = sqlite3.Tsqlite3_vfs{
		FiVersion:          1,
		FszOsFile:          defaultVfs.FszOsFile + int32(unsafe.Sizeof(perFileState{})),
		FmxPathname:        defaultVfs.FmxPathname,
		FpNext:             0,
		FzName:             cname,
		FpAppData:          fs.token,
		FxOpen:             cFuncPointer(xOpenTrampoline),
		FxDelete:           defaultVfs.FxDelete,
		FxAccess:           defaultVfs.FxAccess,
		FxFullPathname:     defaultVfs.FxFullPathname,
		FxDlOpen:           defaultVfs.FxDlOpen,
		FxDlError:          defaultVfs.FxDlError,
		FxDlSym:            defaultVfs.FxDlSym,
		FxDlClose:          defaultVfs.FxDlClose,
		FxRandomness:       defaultVfs.FxRandomness,
		FxSleep:            defaultVfs.FxSleep,
		FxCurrentTime:      defaultVfs.FxCurrentTime,
		FxGetLastError:     defaultVfs.FxGetLastError,
		FxCurrentTimeInt64: defaultVfs.FxCurrentTimeInt64,
		FxSetSystemCall:    defaultVfs.FxSetSystemCall,
		FxGetSystemCall:    defaultVfs.FxGetSystemCall,
		FxNextSystemCall:   defaultVfs.FxNextSystemCall,
	}

	if rc := sqlite3.Xsqlite3_vfs_register(tls, cvfs, 0); rc != sqlite3.SQLITE_OK {
		unregisterFS(fs.token)
		libc.Xfree(tls, cvfs)
		libc.Xfree(tls, cname)
		tls.Close()
		return "", nil, fmt.Errorf("crypto: Xsqlite3_vfs_register rc=%d", rc)
	}
	return name, fs, nil
}

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
	f.tls.Close()
	if rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("crypto: Xsqlite3_vfs_unregister rc=%d", rc)
	}
	return nil
}

func isValidPageSize(n int) bool {
	if n < 512 || n > 65536 {
		return false
	}
	return n&(n-1) == 0
}
