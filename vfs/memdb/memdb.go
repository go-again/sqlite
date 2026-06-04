package memdb

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/go-again/sqlite/internal/cabi"
)

// Options is reserved for future configuration. Currently empty;
// memdb has no tunable knobs at registration time.
type Options struct{}

// FS is a registered in-memory VFS. Each [New] returns a distinct FS;
// call [FS.Close] to unregister and release every named DB it hosts.
type FS struct {
	name   string
	cname  uintptr
	cvfs   uintptr
	tls    *libc.TLS
	token  uintptr
	closed atomic.Bool
	mu     sync.Mutex
	memDBs map[string]*memDB // shared (leading-/) DBs keyed by trimmed name
}

// memDB is the shared per-name page store. Pages are byte slices
// keyed by file offset. Reads and writes lock the per-DB mutex
// briefly; there's no MVCC.
type memDB struct {
	mu    sync.RWMutex
	name  string
	pages map[int64][]byte
	size  int64
	refs  atomic.Int32
}

var stateMu sync.Mutex

// fsRegistry maps the opaque token stored in pVfs->FpAppData back to
// the owning *FS. Promoted into [cabi.Registry] so the four VFS
// sub-packages share one copy of the lookup machinery.
var fsRegistry = cabi.NewRegistry[FS]()

func registerFS(fs *FS) uintptr { return fsRegistry.Register(fs) }
func lookupFS(tok uintptr) *FS  { return fsRegistry.Lookup(tok) }
func unregisterFS(tok uintptr)  { fsRegistry.Unregister(tok) }

// New registers an in-memory VFS and returns its name (slot into a
// DSN as `?vfs=<name>`), a handle for cleanup, and any error.
func New(_ Options) (name string, fs *FS, err error) {
	stateMu.Lock()
	defer stateMu.Unlock()

	tls := libc.NewTLS()
	defPtr := sqlite3.Xsqlite3_vfs_find(tls, 0)
	if defPtr == 0 {
		tls.Close()
		return "", nil, fmt.Errorf("memdb: Xsqlite3_vfs_find(NULL) returned 0; no default VFS")
	}
	defVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(defPtr))
	initOnce.Do(func() { initFromDefault(defVfs) })

	name = cabi.UniqueName("memdb")
	cname, e := libc.CString(name)
	if e != nil {
		tls.Close()
		return "", nil, fmt.Errorf("memdb: alloc name: %w", e)
	}
	cvfs := libc.Xmalloc(tls, libc.Tsize_t(unsafe.Sizeof(sqlite3.Tsqlite3_vfs{})))
	if cvfs == 0 {
		libc.Xfree(tls, cname)
		tls.Close()
		return "", nil, fmt.Errorf("memdb: alloc VFS struct: out of memory")
	}

	fs = &FS{
		name:   name,
		cname:  cname,
		cvfs:   cvfs,
		tls:    tls,
		memDBs: map[string]*memDB{},
	}
	fs.token = registerFS(fs)

	// Per-file size: just enough to hold the base SQLite file struct
	// (FpMethods pointer) followed by our perFileState. memdb never
	// forwards I/O to the default VFS, so we don't need its private
	// tail — and matching the perFileStateOf offset (sizeof
	// Tsqlite3_file) prevents a future maintainer from accidentally
	// overlapping the two regions.
	szOsFile := int32(unsafe.Sizeof(sqlite3.Tsqlite3_file{}) + unsafe.Sizeof(perFileState{}))

	ourVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(cvfs))
	*ourVfs = sqlite3.Tsqlite3_vfs{
		FiVersion:          1,
		FszOsFile:          szOsFile,
		FmxPathname:        defVfs.FmxPathname,
		FpNext:             0,
		FzName:             cname,
		FpAppData:          fs.token,
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

	if rc := sqlite3.Xsqlite3_vfs_register(tls, cvfs, 0); rc != sqlite3.SQLITE_OK {
		unregisterFS(fs.token)
		libc.Xfree(tls, cvfs)
		libc.Xfree(tls, cname)
		tls.Close()
		return "", nil, fmt.Errorf("memdb: vfs_register rc=%d", rc)
	}
	return name, fs, nil
}

// Name returns the registered VFS name.
func (f *FS) Name() string { return f.name }

// Close unregisters the VFS and releases every named DB it hosted.
// Idempotent.
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

	// Drop any fileHandles still owned by this FS. Without this drain,
	// repeated New/Close cycles permanently leak handle records.
	fileHandlesMu.Lock()
	for tok, h := range fileHandles {
		if h.fs == f {
			delete(fileHandles, tok)
		}
	}
	fileHandlesMu.Unlock()

	f.mu.Lock()
	f.memDBs = nil
	f.mu.Unlock()
	if rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("memdb: vfs_unregister rc=%d", rc)
	}
	return nil
}

// acquireDB returns the shared memDB for name (if name begins with
// `/`), or a fresh private memDB otherwise. The bool indicates which
// case applied.
func (f *FS) acquireDB(name string) (*memDB, bool) {
	shared := strings.HasPrefix(name, "/") || strings.HasPrefix(name, "file:/")
	if shared {
		key := strings.TrimPrefix(name, "file:")
		key = strings.TrimLeft(key, "/")
		f.mu.Lock()
		db, ok := f.memDBs[key]
		if !ok {
			db = &memDB{name: key, pages: map[int64][]byte{}}
			f.memDBs[key] = db
		}
		db.refs.Add(1)
		f.mu.Unlock()
		return db, true
	}
	db := &memDB{name: name, pages: map[int64][]byte{}}
	db.refs.Add(1)
	return db, false
}

func (f *FS) releaseDB(db *memDB, shared bool) {
	if !shared {
		// Private DB lives in no map; nothing to protect.
		db.refs.Add(-1)
		return
	}
	// Decrement must happen under f.mu so a concurrent acquireDB
	// can't grab a reference between the decrement and the delete —
	// otherwise the next acquire on this name spawns a second memDB
	// instance and we'd have two live stores under one shared name.
	f.mu.Lock()
	if db.refs.Add(-1) == 0 {
		if cur, ok := f.memDBs[db.name]; ok && cur == db {
			delete(f.memDBs, db.name)
		}
	}
	f.mu.Unlock()
}
