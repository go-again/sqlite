package vfs

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"gosqlite.org/internal/cabi"
)

// WAL shared memory for a custom VFS, owned entirely by the dispatcher.
// A File opts in by implementing [ShmFile]; the only thing the user code
// declares is the sharing key (ShmGroup). Everything below — the C
// memory the WAL index lives in, and the 8-slot lock table SQLite uses
// to coordinate readers, writers, checkpointers, and recovery — is
// managed here, so a user VFS never touches unsafe memory or lock
// protocol details.
//
// Scope: in-process. Connections in the same process that name the same
// group share one shmGroup; there is no cross-process shm (that needs
// real OS shared memory + file locks, which the platform VFS already
// provides).

const shmNLOCK = sqlite3.SQLITE_SHM_NLOCK // 8 WAL lock slots

// shmGroup is the shared-memory backing for one WAL sharing key. Regions
// are C allocations (stable addresses SQLite maps and writes through);
// the lock table arbitrates the WAL protocol across attached files.
type shmGroup struct {
	mu      sync.Mutex
	tls     *libc.TLS // dedicated TLS for region malloc/free (always under mu)
	regions []uintptr // C pointers, one per region index
	sizes   []int32
	shared  [shmNLOCK]int  // count of SHARED holders per slot
	excl    [shmNLOCK]bool // EXCLUSIVE held per slot
	refs    int            // attached files (for lifetime)
}

var (
	shmMu     sync.Mutex
	shmGroups = map[string]*shmGroup{}
)

// attachShm resolves (creating if needed) the shmGroup for of's file and
// pins it to of. Idempotent: a file attaches once, on its first xShmMap.
func (of *openFile) attachShm() *shmGroup {
	if of.shm != nil {
		return of.shm
	}
	sf, ok := of.file.(ShmFile)
	if !ok {
		return nil
	}
	key := sf.ShmGroup()
	shmMu.Lock()
	g := shmGroups[key]
	if g == nil {
		g = &shmGroup{tls: libc.NewTLS()}
		shmGroups[key] = g
	}
	g.refs++
	shmMu.Unlock()
	of.shm = g
	of.shmKey = key
	return g
}

// detachShm drops of's reference to its shmGroup, freeing the group's C
// regions when the last file detaches. Called from xShmUnmap and, as a
// safety net, xClose.
func (of *openFile) detachShm() {
	g := of.shm
	if g == nil {
		return
	}
	of.shm = nil

	shmMu.Lock()
	g.refs--
	last := g.refs == 0
	if last {
		delete(shmGroups, of.shmKey)
	}
	shmMu.Unlock()

	if !last {
		return
	}
	g.mu.Lock()
	for _, r := range g.regions {
		libc.Xfree(g.tls, r)
	}
	g.regions = nil
	g.sizes = nil
	g.mu.Unlock()
	g.tls.Close()
}

// initShmIoMethods builds the iVersion-2 io-methods table shared by
// every shm-capable file: the same 12 base slots as the iVersion-1
// table plus the four xShm* slots. Files whose VFS returns a [ShmFile]
// point here; everyone else stays on the iVersion-1 table.
func initShmIoMethods() {
	ioMethodsShm = sqlite3.Tsqlite3_io_methods{
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
	ioMethodsShmPtr.Store(uintptr(unsafe.Pointer(&ioMethodsShm)))
}

// xShmMap returns (allocating on demand) the iRegion-th shared-memory
// region of size pgsz, writing its C address through pp. bExtend == 0
// means "only if it already exists" — a probe SQLite uses before the
// WAL header is written; we answer with a null pointer, the documented
// "not yet there" signal.
func xShmMapTrampoline(_ *libc.TLS, id uintptr, iRegion, pgsz, bExtend int32, pp uintptr) int32 {
	of := fileFor(id)
	if of == nil {
		return sqlite3.SQLITE_IOERR_SHMMAP
	}
	g := of.attachShm()
	if g == nil {
		return sqlite3.SQLITE_IOERR_SHMMAP
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if int(iRegion) >= len(g.regions) {
		if bExtend == 0 {
			*(*uintptr)(unsafe.Pointer(pp)) = 0
			return sqlite3.SQLITE_OK
		}
		// Allocate every region up to and including iRegion. Xcalloc
		// zero-fills, which the WAL index requires for a fresh region.
		for int(iRegion) >= len(g.regions) {
			mem := libc.Xcalloc(g.tls, 1, libc.Tsize_t(pgsz))
			if mem == 0 {
				*(*uintptr)(unsafe.Pointer(pp)) = 0
				return sqlite3.SQLITE_IOERR_NOMEM
			}
			g.regions = append(g.regions, mem)
			g.sizes = append(g.sizes, pgsz)
		}
	}
	*(*uintptr)(unsafe.Pointer(pp)) = g.regions[iRegion]
	return sqlite3.SQLITE_OK
}

// xShmLock arbitrates the WAL lock protocol over slots [offset,
// offset+n). flags is one of {LOCK,UNLOCK} | {SHARED,EXCLUSIVE}. A
// conflicting request returns SQLITE_BUSY, which SQLite expects and
// retries through the busy handler.
func xShmLockTrampoline(_ *libc.TLS, id uintptr, offset, n, flags int32) int32 {
	of := fileFor(id)
	if of == nil {
		return sqlite3.SQLITE_IOERR_SHMLOCK
	}
	g := of.shm
	if g == nil {
		return sqlite3.SQLITE_IOERR_SHMLOCK
	}
	lo, hi := int(offset), int(offset+n)
	if lo < 0 || hi > shmNLOCK || lo >= hi {
		return sqlite3.SQLITE_IOERR_SHMLOCK
	}

	unlock := flags&sqlite3.SQLITE_SHM_UNLOCK != 0
	shared := flags&sqlite3.SQLITE_SHM_SHARED != 0

	g.mu.Lock()
	defer g.mu.Unlock()

	if unlock {
		for i := lo; i < hi; i++ {
			if shared {
				if g.shared[i] > 0 {
					g.shared[i]--
				}
			} else {
				g.excl[i] = false
			}
		}
		return sqlite3.SQLITE_OK
	}

	if shared {
		for i := lo; i < hi; i++ {
			if g.excl[i] {
				return sqlite3.SQLITE_BUSY
			}
		}
		for i := lo; i < hi; i++ {
			g.shared[i]++
		}
		return sqlite3.SQLITE_OK
	}

	// exclusive
	for i := lo; i < hi; i++ {
		if g.excl[i] || g.shared[i] > 0 {
			return sqlite3.SQLITE_BUSY
		}
	}
	for i := lo; i < hi; i++ {
		g.excl[i] = true
	}
	return sqlite3.SQLITE_OK
}

// xShmBarrier is a full memory barrier on real hardware shm. The Go
// memory model already orders our mutex-guarded region access, so the
// barrier is a no-op here.
func xShmBarrierTrampoline(_ *libc.TLS, _ uintptr) {}

// xShmUnmap detaches this file from its shm group, freeing the group's
// regions when the last connection leaves. deleteFlag is advisory for
// in-memory shm — refcount drives the actual free.
func xShmUnmapTrampoline(_ *libc.TLS, id uintptr, _ int32) int32 {
	of := fileFor(id)
	if of == nil {
		return sqlite3.SQLITE_OK
	}
	of.detachShm()
	return sqlite3.SQLITE_OK
}

var (
	ioMethodsShm    sqlite3.Tsqlite3_io_methods
	ioMethodsShmPtr atomic.Uintptr
)
