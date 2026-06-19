package memdb

import (
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"

	"gosqlite.org/vfs/internal/memio"
)

func xCloseTrampoline(_ *libc.TLS, pFile uintptr) int32 {
	pst := perFileStateOf(pFile)
	h := handleFor(pFile)
	if h == nil {
		return sqlite3.SQLITE_OK
	}
	dropHandle(pst.token)
	pst.token = 0
	if h.db != nil {
		h.fs.releaseDB(h.db, h.shared)
	}
	return sqlite3.SQLITE_OK
}

// xRead reads bytes directly from the per-DB page map. No snapshot,
// no isolation — readers and writers race on the per-db RWMutex.
func xReadTrampoline(_ *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	h := handleFor(pFile)
	if h == nil {
		return sqlite3.SQLITE_IOERR_READ
	}
	h.db.mu.RLock()
	defer h.db.mu.RUnlock()

	dst := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(amt))
	for i := range dst {
		dst[i] = 0
	}
	end := int64(off) + int64(amt)
	n := memio.ReadFromPages(h.db.pages, int64(off), end, dst)
	if end > h.db.size {
		return sqlite3.SQLITE_IOERR_SHORT_READ
	}
	if n < amt {
		return sqlite3.SQLITE_IOERR_SHORT_READ
	}
	return sqlite3.SQLITE_OK
}

// xWrite stores the page directly. Future reads — including those
// from already-open handles — see the new bytes immediately.
func xWriteTrampoline(_ *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	h := handleFor(pFile)
	if h == nil {
		return sqlite3.SQLITE_IOERR_WRITE
	}
	if h.readOnly {
		return sqlite3.SQLITE_READONLY
	}
	src := unsafe.Slice((*byte)(unsafe.Pointer(buf)), int(amt))
	page := make([]byte, amt)
	copy(page, src)

	h.db.mu.Lock()
	defer h.db.mu.Unlock()
	h.db.pages[int64(off)] = page
	if e := int64(off) + int64(amt); e > h.db.size {
		h.db.size = e
	}
	return sqlite3.SQLITE_OK
}

func xTruncateTrampoline(_ *libc.TLS, pFile uintptr, size sqlite3.Tsqlite3_int64) int32 {
	h := handleFor(pFile)
	if h == nil {
		return sqlite3.SQLITE_IOERR_TRUNCATE
	}
	if h.readOnly {
		return sqlite3.SQLITE_READONLY
	}
	h.db.mu.Lock()
	defer h.db.mu.Unlock()
	for k, v := range h.db.pages {
		if k >= int64(size) {
			delete(h.db.pages, k)
			continue
		}
		if k+int64(len(v)) > int64(size) {
			h.db.pages[k] = v[:int64(size)-k]
		}
	}
	h.db.size = int64(size)
	return sqlite3.SQLITE_OK
}

// xSync is a no-op — memdb has no on-disk presence to flush.
func xSyncTrampoline(_ *libc.TLS, _ uintptr, _ int32) int32 {
	return sqlite3.SQLITE_OK
}

func xFileSizeTrampoline(_ *libc.TLS, pFile, pSize uintptr) int32 {
	h := handleFor(pFile)
	if h == nil {
		*(*sqlite3.Tsqlite3_int64)(unsafe.Pointer(pSize)) = 0
		return sqlite3.SQLITE_OK
	}
	h.db.mu.RLock()
	sz := h.db.size
	h.db.mu.RUnlock()
	*(*sqlite3.Tsqlite3_int64)(unsafe.Pointer(pSize)) = sqlite3.Tsqlite3_int64(sz)
	return sqlite3.SQLITE_OK
}

// xLock / xUnlock are no-ops; SQLite's process-internal locking is
// sufficient because every operation goes through the per-db RWMutex.
func xLockTrampoline(_ *libc.TLS, _ uintptr, _ int32) int32 {
	return sqlite3.SQLITE_OK
}

func xUnlockTrampoline(_ *libc.TLS, _ uintptr, _ int32) int32 {
	return sqlite3.SQLITE_OK
}

func xCheckReservedLockTrampoline(_ *libc.TLS, _ uintptr, pResOut uintptr) int32 {
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
	// SQLITE_IOCAP_ATOMIC | SQLITE_IOCAP_POWERSAFE_OVERWRITE — every
	// write either lands or doesn't (memory swap); no torn-write risk.
	return 0x0001 | 0x1000
}
