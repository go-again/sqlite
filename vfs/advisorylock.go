package vfs

import (
	"sync"

	sqlite3 "modernc.org/sqlite/lib"
)

// errLockBusy is returned when a requested advisory lock conflicts with another
// connection's lock on the same file.
var errLockBusy = &VFSError{Code: sqlite3.SQLITE_BUSY}

// AdvisoryLock implements SQLite's in-process advisory file-locking protocol,
// shared by every [File] handle over the same underlying storage. Many handles may
// hold SHARED; only one may hold RESERVED..EXCLUSIVE; EXCLUSIVE additionally
// requires that no other handle holds SHARED. It is the arbitration a pure-Go VFS
// needs when several database/sql connections open one in-process file.
//
// Hold ONE AdvisoryLock in the object shared by all handles of a file (the
// in-memory database, the on-disk container, …), keep each handle's current
// [LockLevel] on the handle, and forward the handle's Lock/Unlock/CheckReservedLock
// to the matching method here — passing the handle itself as self (compared by
// identity) and a pointer to its level (updated in place):
//
//	func (f *myFile) Lock(l vfs.LockLevel) error        { return f.shared.lk.Lock(f, &f.lock, l) }
//	func (f *myFile) Unlock(l vfs.LockLevel) error      { return f.shared.lk.Unlock(f, &f.lock, l) }
//	func (f *myFile) CheckReservedLock() (bool, error)  { return f.shared.lk.CheckReservedLock() }
//
// The zero value is ready to use. The lock is in-process only (it coordinates
// goroutines within one program, not separate OS processes).
type AdvisoryLock struct {
	mu          sync.Mutex
	nShared     int        // handles currently holding >= SHARED
	writer      any        // the handle holding RESERVED..EXCLUSIVE, or nil
	writerLevel *LockLevel // that handle's current level, read to arbitrate
}

// Lock raises the calling handle's level (*cur) toward level.
func (a *AdvisoryLock) Lock(self any, cur *LockLevel, level LockLevel) error {
	if level <= *cur {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch level {
	case LockShared:
		if a.writer != nil && *a.writerLevel >= LockPending {
			return errLockBusy // a PENDING/EXCLUSIVE writer blocks new readers
		}
		a.nShared++
		*cur = LockShared
	case LockReserved:
		if a.writer != nil && a.writer != self {
			return errLockBusy
		}
		a.writer, a.writerLevel = self, cur
		*cur = LockReserved
	case LockPending, LockExclusive:
		if a.writer != nil && a.writer != self {
			return errLockBusy
		}
		a.writer, a.writerLevel = self, cur
		held := 0
		if *cur >= LockShared {
			held = 1
		}
		if a.nShared > held {
			*cur = LockPending // hold the intent so no new SHARED is granted
			return errLockBusy
		}
		*cur = level
	}
	return nil
}

// Unlock lowers the calling handle's level (*cur) toward level.
func (a *AdvisoryLock) Unlock(self any, cur *LockLevel, level LockLevel) error {
	if level >= *cur {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if *cur >= LockReserved && level < LockReserved && a.writer == self {
		a.writer, a.writerLevel = nil, nil
	}
	if *cur >= LockShared && level < LockShared {
		a.nShared--
	}
	*cur = level
	return nil
}

// CheckReservedLock reports whether some handle holds RESERVED or higher.
func (a *AdvisoryLock) CheckReservedLock() (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.writer != nil && *a.writerLevel >= LockReserved, nil
}
