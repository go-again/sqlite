package crypto

import (
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/go-again/sqlite/internal/cabi"
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

// String renders a Cipher as a human label suitable for logs and
// table-driven tests. The values match the canonical names used
// throughout the package's docs.
func (c Cipher) String() string {
	switch c {
	case Adiantum:
		return "Adiantum"
	case AESXTS:
		return "AES-XTS"
	}
	return fmt.Sprintf("Cipher(%d)", int(c))
}

// Options configures [New].
type Options struct {
	// Key is the raw cipher key. Length depends on Cipher: 32 bytes
	// for Adiantum, 64 bytes for AES-XTS-256. New() takes a
	// defensive copy, so the caller is free to zero or reuse the
	// slice as soon as New returns.
	Key []byte

	// Cipher picks the mode. Defaults to Adiantum.
	Cipher Cipher

	// PageSize must match the database's PRAGMA page_size. Defaults
	// to 4096 (SQLite's default). Set explicitly when the DB was
	// created with a non-default page size. Must be a power of two
	// between 512 and 65536 inclusive.
	PageSize int

	// Recorder, if non-nil, receives one OnRead / OnWrite event per
	// xRead / xWrite trampoline invocation. See [Recorder] and
	// [NewSlogRecorder]. Nil means no recording.
	Recorder Recorder

	// WrapVFS is the name of an existing registered VFS that this
	// crypto VFS should layer on top of (the wrapped VFS receives our
	// post-encryption pages and is responsible for actually putting
	// them on disk). Empty means wrap the system default VFS, which
	// is the standalone-encryption case. Set to a [vfs/cksm] name to
	// get "encrypted-then-checksummed" composition.
	WrapVFS string
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
	recorder Recorder // optional; nil = no observability
	token    uintptr  // registry handle, stored in pVfs->FpAppData
	closed   atomic.Bool

	// ourIoMethods lives in its own heap allocation rather than
	// inline on FS. Go's checkptr (-race) is happier when the
	// methods table is a discrete Tsqlite3_io_methods allocation than
	// a field inside a larger struct — modernc's transpiled lib does
	// pointer arithmetic against the table internally.
	ourIoMethods *sqlite3.Tsqlite3_io_methods

	// wrappedVfsPtr is the Tsqlite3_vfs we forward xOpen to. With
	// Options.WrapVFS == "" this is the system default VFS; with a
	// non-empty WrapVFS it is whatever Xsqlite3_vfs_find returned for
	// that name (typically another vfs/cksm or vfs/crypto).
	wrappedVfsPtr uintptr
	// wrappedSzOsFile is the wrapped VFS's szOsFile. Our perFileState
	// lives at this offset from pFile, so each chained layer can find
	// its own slot without colliding with the layer beneath it.
	wrappedSzOsFile int32
}

const defaultPageSize = 4096

// stateMu serializes [New] and [FS.Close] so libc TLS setup and the
// global VFS registration table aren't raced.
var stateMu sync.Mutex

// fsRegistry maps the opaque token stored in pVfs->FpAppData (and
// later copied into per-file state) back to the *FS that owns the
// cipher. Promoted into [cabi.Registry] so the four VFS sub-packages
// share one copy of the lookup machinery.
var fsRegistry = cabi.NewRegistry[FS]()

func registerFS(fs *FS) uintptr { return fsRegistry.Register(fs) }
func lookupFS(tok uintptr) *FS  { return fsRegistry.Lookup(tok) }
func unregisterFS(tok uintptr)  { fsRegistry.Unregister(tok) }

// New registers an encryption VFS and returns its name (slot into a
// DSN as `?vfs=<name>`), a handle for cleanup, and any error.
//
// Each call registers a distinct VFS. Calls from multiple goroutines
// are safe — a package-level mutex serializes them. Adiantum
// (default) and AES-XTS-256 are both supported.
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
	// Find the VFS to wrap: by name when WrapVFS is set, otherwise the
	// system default. Wrapping a named VFS is what lets crypto layer
	// on top of vfs/cksm (cksm registers as "cksmN"; pass that name
	// here).
	var defaultVfsPtr uintptr
	if opts.WrapVFS != "" {
		cName, allocErr := libc.CString(opts.WrapVFS)
		if allocErr != nil {
			tls.Close()
			return "", nil, fmt.Errorf("crypto: alloc WrapVFS name: %w", allocErr)
		}
		defaultVfsPtr = sqlite3.Xsqlite3_vfs_find(tls, cName)
		libc.Xfree(tls, cName)
		if defaultVfsPtr == 0 {
			tls.Close()
			return "", nil, fmt.Errorf("crypto: WrapVFS %q not registered", opts.WrapVFS)
		}
	} else {
		defaultVfsPtr = sqlite3.Xsqlite3_vfs_find(tls, 0)
		if defaultVfsPtr == 0 {
			tls.Close()
			return "", nil, fmt.Errorf("crypto: Xsqlite3_vfs_find(NULL) returned 0; no default VFS registered")
		}
	}
	defaultVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(defaultVfsPtr))

	name = cabi.UniqueName("crypto")
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
		name:            name,
		cname:           cname,
		cvfs:            cvfs,
		tls:             tls,
		cipher:          cipher,
		pageSize:        int32(pageSize),
		recorder:        opts.Recorder,
		wrappedVfsPtr:   defaultVfsPtr,
		wrappedSzOsFile: defaultVfs.FszOsFile,
	}
	if err := fs.initIoMethods(); err != nil {
		// initIoMethods allocates an io-methods table via libc.Xmalloc;
		// failure means the table didn't land, but the cname + cvfs
		// allocations and the tls handle above are still live. Release
		// them so a long-running app that retries New() with degraded
		// memory doesn't leak per failed attempt.
		libc.Xfree(tls, cvfs)
		libc.Xfree(tls, cname)
		tls.Close()
		return "", nil, err
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
		FxOpen:             cabi.FuncPointer(xOpenTrampoline),
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
		if fs.ourIoMethods != nil {
			libc.Xfree(tls, uintptr(unsafe.Pointer(fs.ourIoMethods)))
		}
		libc.Xfree(tls, cvfs)
		libc.Xfree(tls, cname)
		tls.Close()
		return "", nil, fmt.Errorf("crypto: Xsqlite3_vfs_register rc=%d", rc)
	}
	return name, fs, nil
}

// Name returns the registered VFS name, the same string returned as
// the first value from [New]. Useful when a caller wants to thread
// just the *FS around (e.g. via dependency injection) and build the
// DSN at the point of sql.Open.
func (fs *FS) Name() string { return fs.name }

// Close unregisters the VFS and frees its libc allocations. Idempotent.
func (fs *FS) Close() error {
	if fs.closed.Swap(true) {
		return nil
	}
	stateMu.Lock()
	defer stateMu.Unlock()

	rc := sqlite3.Xsqlite3_vfs_unregister(fs.tls, fs.cvfs)
	unregisterFS(fs.token)
	libc.Xfree(fs.tls, fs.cvfs)
	libc.Xfree(fs.tls, fs.cname)
	if fs.ourIoMethods != nil {
		libc.Xfree(fs.tls, uintptr(unsafe.Pointer(fs.ourIoMethods)))
		fs.ourIoMethods = nil
	}
	fs.tls.Close()
	// NOTE: we deliberately do NOT zero out fs.cipher here. A previous
	// version did, motivated by "shorten the GC window before the key
	// bytes become reclaimable", but it opened a race: a trampoline
	// that resolved its *FS via fsFor() just before Close ran would
	// later dereference fs.cipher inside readEncrypted / writeEncrypted
	// and hit a nil-receiver panic. The threat model already excludes
	// live-memory attacks (see doc.go); the GC reclaims the cipher
	// (and the defensive-copied key inside it) when the *FS itself
	// becomes unreachable, which is good enough.
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
