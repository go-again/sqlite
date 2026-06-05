package mvcc

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

// Options configures a [New] registration. Currently empty; reserved
// for forward compatibility with cipher/checksum composition.
type Options struct{}

// FS is a registered in-memory MVCC VFS. Each [New] returns a distinct
// FS; call [FS.Close] to unregister and release every named DB it
// hosts.
type FS struct {
	name   string
	cname  uintptr
	cvfs   uintptr
	tls    *libc.TLS
	token  uintptr
	closed atomic.Bool
	mu     sync.Mutex
	memDBs map[string]*memDB // shared (leading-/) DBs
}

// memDB is the shared per-name in-memory database. Pages are byte
// slices keyed by file offset; the current snapshot's map is held
// behind an atomic.Pointer so readers acquire an O(1) consistent view
// at lock-acquire time.
type memDB struct {
	name    string
	snap    atomic.Pointer[snapshot]
	writeMu sync.Mutex // serializes writers (one at a time)
	refs    atomic.Int32
}

// snapshot is an immutable view of a memDB at a moment in time.
type snapshot struct {
	pages map[int64][]byte // offset → page bytes (logically immutable)
	size  int64            // file size
}

var stateMu sync.Mutex

// fsRegistry maps the opaque token stored in pVfs->FpAppData back to
// the owning *FS. Promoted into [cabi.Registry] so the four VFS
// sub-packages share one copy of the lookup machinery.
var fsRegistry = cabi.NewRegistry[FS]()

func registerFS(fs *FS) uintptr { return fsRegistry.Register(fs) }
func lookupFS(tok uintptr) *FS  { return fsRegistry.Lookup(tok) }
func unregisterFS(tok uintptr)  { fsRegistry.Unregister(tok) }

// New registers an MVCC VFS and returns its name (slot into a DSN as
// `?vfs=<name>`), a handle for cleanup, and any error.
func New(_ Options) (name string, fs *FS, err error) {
	stateMu.Lock()
	defer stateMu.Unlock()

	tls := libc.NewTLS()
	defPtr := sqlite3.Xsqlite3_vfs_find(tls, 0)
	if defPtr == 0 {
		tls.Close()
		return "", nil, fmt.Errorf("mvcc: Xsqlite3_vfs_find(NULL) returned 0; no default VFS")
	}
	defVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(defPtr))
	initOnce.Do(func() { initFromDefault(defVfs) })

	name = cabi.UniqueName("mvcc")
	cname, e := libc.CString(name)
	if e != nil {
		tls.Close()
		return "", nil, fmt.Errorf("mvcc: alloc name: %w", e)
	}
	cvfs := libc.Xmalloc(tls, libc.Tsize_t(unsafe.Sizeof(sqlite3.Tsqlite3_vfs{})))
	if cvfs == 0 {
		libc.Xfree(tls, cname)
		tls.Close()
		return "", nil, fmt.Errorf("mvcc: alloc VFS struct: out of memory")
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
	// (FpMethods pointer) followed by our perFileState. mvcc never
	// forwards I/O to the default VFS, so we don't need its private
	// tail — and matching the perFileStateOf offset (sizeof
	// Tsqlite3_file) prevents a future maintainer from accidentally
	// overlapping the two regions.
	szOsFile := int32(unsafe.Sizeof(sqlite3.Tsqlite3_file{}) + unsafe.Sizeof(perFileState{}))

	ourVfs := (*sqlite3.Tsqlite3_vfs)(unsafe.Pointer(cvfs))
	*ourVfs = sqlite3.Tsqlite3_vfs{
		FiVersion:   1,
		FszOsFile:   szOsFile,
		FmxPathname: defVfs.FmxPathname,
		FpNext:      0,
		FzName:      cname,
		FpAppData:   fs.token,
		FxOpen:      cabi.FuncPointer(xOpenTrampoline),
		FxDelete:    cabi.FuncPointer(xDeleteTrampoline),
		FxAccess:    cabi.FuncPointer(xAccessTrampoline),
		// FullPathname must be ours — the default VFS would resolve the
		// name against the host filesystem (absolute-path resolution,
		// symlink walk), which is meaningless for an in-memory store
		// and would also collapse our shared/private name conventions.
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
		return "", nil, fmt.Errorf("mvcc: vfs_register rc=%d", rc)
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
	fileHandles.DeleteWhere(func(_ uintptr, h *fileHandle) bool {
		return h.fs == f
	})

	f.mu.Lock()
	f.memDBs = nil
	f.mu.Unlock()
	if rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("mvcc: vfs_unregister rc=%d", rc)
	}
	return nil
}

// shared returns the shared memDB for name; if shared==false, returns
// a newly-allocated private memDB. Caller MUST decrement refs (or call
// release) when done.
func (f *FS) acquireDB(name string) (*memDB, bool) {
	shared := strings.HasPrefix(name, "/") || strings.HasPrefix(name, "file:/")
	if shared {
		// Trim leading slashes / file: prefixes for stable keys.
		key := strings.TrimPrefix(name, "file:")
		key = strings.TrimLeft(key, "/")
		f.mu.Lock()
		db, ok := f.memDBs[key]
		if !ok {
			db = &memDB{name: key}
			db.snap.Store(&snapshot{pages: map[int64][]byte{}})
			f.memDBs[key] = db
		}
		db.refs.Add(1)
		f.mu.Unlock()
		return db, true
	}
	// Private — fresh DB, no sharing.
	db := &memDB{name: name}
	db.snap.Store(&snapshot{pages: map[int64][]byte{}})
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
	// and we'd have two live snapshot stores under one shared name.
	f.mu.Lock()
	if db.refs.Add(-1) == 0 {
		if cur, ok := f.memDBs[db.name]; ok && cur == db {
			delete(f.memDBs, db.name)
		}
	}
	f.mu.Unlock()
}
