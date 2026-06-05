package mvcc

import (
	"maps"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/go-again/sqlite/vfs/internal/memio"
)

// SQLite lock levels (mirror sqlite3.SQLITE_LOCK_*).
const (
	lockNone      int32 = 0
	lockShared    int32 = 1
	lockReserved  int32 = 2
	lockPending   int32 = 3
	lockExclusive int32 = 4
)

// xClose drops the file handle; releases the shared DB ref and
// abandons any uncommitted writes.
func xCloseTrampoline(_ *libc.TLS, pFile uintptr) int32 {
	pst := perFileStateOf(pFile)
	h := handleFor(pFile)
	if h == nil {
		return sqlite3.SQLITE_OK
	}
	// Drop any lock the handle still holds. SQLite normally walks the
	// lock state machine down to NONE before xClose, but force-cleanup
	// paths (context cancel mid-tx, abnormal Close) can skip xUnlock.
	// Leaking writeMu would deadlock the next writer on a shared DB.
	h.mu.Lock()
	if h.lockLvl >= lockReserved && h.db != nil {
		h.db.writeMu.Unlock()
	}
	h.lockLvl = lockNone
	h.mu.Unlock()
	dropHandle(pst.token)
	pst.token = 0
	if h.db != nil {
		h.fs.releaseDB(h.db, h.shared)
	}
	return sqlite3.SQLITE_OK
}

// xRead resolves the current snapshot, copies bytes for the requested
// range into the caller buffer, zero-fills any missing tail, and
// reports IOERR_SHORT_READ when the read straddled the end.
func xReadTrampoline(_ *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	h := handleFor(pFile)
	if h == nil {
		return sqlite3.SQLITE_IOERR_READ
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	snap := h.snap
	pendingWrites := h.writeBuf
	pendingSize := h.writeSz
	if snap == nil {
		snap = h.db.snap.Load()
	}

	dst := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(amt))
	// Zero so any gaps surface as zeroed bytes (SQLite expects this on
	// SHORT_READ).
	for i := range dst {
		dst[i] = 0
	}

	maxSeen := max(snap.size, pendingSize)
	end := int64(off) + int64(amt)
	// Order preserved from the original: copy any pending writes, then
	// the snapshot pages, both into the same dst buffer. Pending and
	// snapshot share page keys (the write buffer is cloned from the
	// snapshot before being layered on), so the second copy wins on
	// overlap; do not reorder without revisiting that interaction.
	var n int32
	if pendingWrites != nil {
		n = memio.ReadFromPages(pendingWrites, int64(off), end, dst)
	}
	if sn := memio.ReadFromPages(snap.pages, int64(off), end, dst); sn > n {
		n = sn
	}

	if end > maxSeen {
		return sqlite3.SQLITE_IOERR_SHORT_READ
	}
	if n < amt {
		return sqlite3.SQLITE_IOERR_SHORT_READ
	}
	return sqlite3.SQLITE_OK
}

// xWrite buffers writes in the file handle until commit. The first
// write since the handle was opened (or since the last commit) clones
// the current snapshot into the write buffer so the writer always
// works against a stable base.
func xWriteTrampoline(_ *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	h := handleFor(pFile)
	if h == nil {
		return sqlite3.SQLITE_IOERR_WRITE
	}
	if h.readOnly {
		return sqlite3.SQLITE_READONLY
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ensureWriteBuf()

	src := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(amt))
	page := make([]byte, amt)
	copy(page, src)
	h.writeBuf[int64(off)] = page
	if e := int64(off) + int64(amt); e > h.writeSz {
		h.writeSz = e
	}
	h.hasWrite = true
	return sqlite3.SQLITE_OK
}

func (h *fileHandle) ensureWriteBuf() {
	if h.writeBuf != nil {
		return
	}
	base := h.snap
	if base == nil {
		base = h.db.snap.Load()
	}
	h.writeBuf = make(map[int64][]byte, len(base.pages))
	maps.Copy(h.writeBuf, base.pages)
	h.writeSz = base.size
}

// xTruncate buffers the new size; effective at commit.
func xTruncateTrampoline(_ *libc.TLS, pFile uintptr, size sqlite3.Tsqlite3_int64) int32 {
	h := handleFor(pFile)
	if h == nil {
		return sqlite3.SQLITE_IOERR_TRUNCATE
	}
	if h.readOnly {
		return sqlite3.SQLITE_READONLY
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ensureWriteBuf()
	// Drop any pending page that starts at or past the new size; trim
	// the last partial page that straddles the boundary.
	for k, v := range h.writeBuf {
		if k >= int64(size) {
			delete(h.writeBuf, k)
			continue
		}
		if k+int64(len(v)) > int64(size) {
			h.writeBuf[k] = v[:int64(size)-k]
		}
	}
	h.writeSz = int64(size)
	h.hasWrite = true
	return sqlite3.SQLITE_OK
}

// xSync commits any buffered writes to the shared snapshot.
func xSyncTrampoline(_ *libc.TLS, pFile uintptr, _ int32) int32 {
	h := handleFor(pFile)
	if h == nil {
		return sqlite3.SQLITE_OK
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.hasWrite || h.writeBuf == nil {
		return sqlite3.SQLITE_OK
	}
	// Atomic publish.
	newSnap := &snapshot{pages: h.writeBuf, size: h.writeSz}
	h.db.snap.Store(newSnap)
	h.writeBuf = nil
	h.hasWrite = false
	h.snap = nil // re-read from db on next read
	return sqlite3.SQLITE_OK
}

func xFileSizeTrampoline(_ *libc.TLS, pFile, pSize uintptr) int32 {
	h := handleFor(pFile)
	if h == nil {
		*(*sqlite3.Tsqlite3_int64)(unsafe.Pointer(pSize)) = 0
		return sqlite3.SQLITE_OK
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	sz := h.writeSz
	snap := h.snap
	if snap == nil {
		snap = h.db.snap.Load()
	}
	if snap.size > sz {
		sz = snap.size
	}
	*(*sqlite3.Tsqlite3_int64)(unsafe.Pointer(pSize)) = sqlite3.Tsqlite3_int64(sz)
	return sqlite3.SQLITE_OK
}

// xLock implements a simple SQLite-style lock state machine. Shared
// readers cache a snapshot; writers serialize on the per-DB writeMu.
func xLockTrampoline(_ *libc.TLS, pFile uintptr, level int32) int32 {
	h := handleFor(pFile)
	if h == nil {
		return sqlite3.SQLITE_IOERR_LOCK
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if level <= h.lockLvl {
		return sqlite3.SQLITE_OK
	}
	if level >= lockReserved && h.lockLvl < lockReserved {
		if !h.db.writeMu.TryLock() {
			return sqlite3.SQLITE_BUSY
		}
		// Refresh the snapshot reference at the moment we acquire the
		// writer lock so writes are layered on top of the latest
		// published state, not the stale snapshot captured back at
		// lockShared. Without this refresh, a concurrent writer that
		// committed while we held lockShared would have its changes
		// silently overwritten by our publish on xSync.
		h.snap = h.db.snap.Load()
	}
	if level >= lockShared && h.lockLvl < lockShared {
		// Capture snapshot for the duration of the read transaction.
		h.snap = h.db.snap.Load()
	}
	h.lockLvl = level
	return sqlite3.SQLITE_OK
}

func xUnlockTrampoline(_ *libc.TLS, pFile uintptr, level int32) int32 {
	h := handleFor(pFile)
	if h == nil {
		return sqlite3.SQLITE_OK
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if level >= h.lockLvl {
		return sqlite3.SQLITE_OK
	}
	if h.lockLvl >= lockReserved && level < lockReserved {
		// Releasing the writer lock; if we never committed,
		// abandon the write buffer.
		if h.hasWrite {
			h.writeBuf = nil
			h.writeSz = 0
			h.hasWrite = false
		}
		h.db.writeMu.Unlock()
	}
	if level == lockNone {
		h.snap = nil
	}
	h.lockLvl = level
	return sqlite3.SQLITE_OK
}

func xCheckReservedLockTrampoline(_ *libc.TLS, _ uintptr, pResOut uintptr) int32 {
	// We never grant RESERVED to two handles, so any caller that holds
	// it asked us for it; this method polls "is anyone else holding
	// RESERVED?" so reporting 0 is correct.
	*(*int32)(unsafe.Pointer(pResOut)) = 0
	return sqlite3.SQLITE_OK
}

func xFileControlTrampoline(_ *libc.TLS, _ uintptr, _ int32, _ uintptr) int32 {
	return sqlite3.SQLITE_NOTFOUND
}

func xSectorSizeTrampoline(_ *libc.TLS, _ uintptr) int32 {
	return 4096
}

func xDeviceCharacteristicsTrampoline(_ *libc.TLS, _ uintptr) int32 {
	// SQLITE_IOCAP_ATOMIC | SQLITE_IOCAP_POWERSAFE_OVERWRITE — our
	// writes either land entirely or not at all (memory swap), and
	// there's no torn-write concern.
	return 0x0001 | 0x1000
}
