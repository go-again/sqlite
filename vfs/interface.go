package vfs

import (
	"io"

	sqlite3 "modernc.org/sqlite/lib"
)

// VFS is a user-implementable SQLite virtual file system. Register an
// implementation with [Register]; reference it from a DSN as
// `?vfs=<name>` (or via sqlite.Config). The dispatcher drives an
// arbitrary VFS through the same battle-tested trampolines that back
// the built-in vfs/memdb and vfs/mvcc backends — implementing this
// interface is all a downstream backend (object store, fault injector,
// tmpfs-on-a-budget, …) needs.
//
// Concurrency: SQLite calls these methods on the goroutine driving the
// owning database/sql connection, but a single VFS instance registered
// under one name can see concurrent Open calls from different pooled
// connections. An implementation shared across connections MUST be safe
// for concurrent use; the dispatcher's own bookkeeping already is.
//
// Phase 1 supports rollback-journal databases. WAL needs the
// shared-memory methods (a future ShmFile capability interface); until
// then, open a custom-VFS database in journal mode.
type VFS interface {
	// Open opens (or creates) the named file. flags is the SQLite open
	// bitset (MainDB / WAL / TempDB / Create / ReadOnly / …). Return the
	// File plus the flags actually granted — e.g. a read-only backend
	// downgrades by clearing OpenReadWrite and setting OpenReadOnly. A
	// zero granted value is treated as "same as requested".
	//
	// name is empty for anonymous temp files (SQLite passes a NULL
	// path); such a file must not be shared and is never reopened.
	Open(name string, flags OpenFlags) (file File, granted OpenFlags, err error)

	// Delete removes the named file. syncDir asks for the containing
	// directory to be fsync'd after the unlink (durable delete). Return
	// a *VFSError wrapping fs.ErrNotExist (or just fs.ErrNotExist) for a
	// missing file; the dispatcher maps it to SQLITE_IOERR_DELETE_NOENT.
	Delete(name string, syncDir bool) error

	// Access reports whether name is accessible for the given op
	// (AccessExists / AccessReadWrite / AccessRead). Return (false, nil)
	// for "not present / not permitted"; reserve a non-nil error for a
	// genuine I/O failure.
	Access(name string, op AccessOp) (bool, error)

	// FullPathname canonicalises name to an absolute path SQLite uses as
	// the cache key that ties a database to its journal/WAL siblings.
	// A backend with a flat namespace may return name unchanged.
	FullPathname(name string) (string, error)
}

// File is one open database / rollback-journal / temp file. The
// dispatcher copies every buffer at the C boundary, so a method never
// sees memory that outlives the call: ReadAt is handed a fresh slice to
// fill, WriteAt a fresh copy of the page to store.
//
// ReadAt follows the [io.ReaderAt] contract with one SQLite nuance: a
// read past end-of-file must return the bytes available plus a short
// count and io.EOF (or a [VFSError]); the dispatcher zero-fills the
// remainder of SQLite's buffer and reports SQLITE_IOERR_SHORT_READ,
// exactly as the native os VFS does. WriteAt follows [io.WriterAt].
//
// Embed [NoLock] to satisfy Lock/Unlock/CheckReservedLock with
// accept-everything semantics — correct for any single-process or
// exclusive-access backend (what memdb effectively does).
type File interface {
	io.ReaderAt // ReadAt(p, off): short read past EOF → zero-fill + SHORT_READ
	io.WriterAt // WriteAt(p, off)

	// Truncate resizes the file to exactly size bytes.
	Truncate(size int64) error
	// Sync flushes buffered writes to durable storage. flags carries
	// the SQLite sync level (SyncNormal / SyncFull, optionally
	// |SyncDataOnly). In-memory backends make this a no-op.
	Sync(flags SyncFlags) error
	// Size reports the current file size in bytes.
	Size() (int64, error)

	// Lock raises the advisory lock to at least level; Unlock lowers it.
	// A single-process backend can accept every transition (see NoLock).
	Lock(level LockLevel) error
	Unlock(level LockLevel) error
	// CheckReservedLock reports whether some connection (possibly
	// another process) holds a RESERVED-or-higher lock.
	CheckReservedLock() (bool, error)

	// SectorSize is the underlying device's atomic-write granularity
	// (bytes); 512 or 4096 are typical. DeviceCharacteristics advertises
	// IOCAP_* guarantees (atomic writes, power-safe overwrite, …) that
	// let SQLite skip journal steps.
	SectorSize() int
	DeviceCharacteristics() DeviceFlags

	// Close releases the file. For a file opened with OpenDeleteOnClose
	// the dispatcher also best-effort calls VFS.Delete afterwards.
	Close() error
}

// FileControl is an optional capability interface a File may implement
// to handle sqlite3_file_control opcodes (PRAGMA passthrough, VFS-name
// queries, …). When a File does not implement it, the dispatcher
// answers SQLITE_NOTFOUND, which SQLite treats as "unhandled".
type FileControl interface {
	// FileControl handles opcode op. arg is the opcode's raw C pointer
	// argument. Return a *VFSError to surface a specific result code; a
	// nil error means handled (SQLITE_OK). Returning ErrNotFound (the
	// default behaviour) tells SQLite the opcode is unhandled.
	FileControl(op int, arg uintptr) error
}

// OpenFlags is the SQLite open bitset passed to VFS.Open and returned
// as the granted set.
type OpenFlags int

// Open* mirror the SQLITE_OPEN_* bits an implementation inspects.
const (
	OpenReadOnly      OpenFlags = sqlite3.SQLITE_OPEN_READONLY
	OpenReadWrite     OpenFlags = sqlite3.SQLITE_OPEN_READWRITE
	OpenCreate        OpenFlags = sqlite3.SQLITE_OPEN_CREATE
	OpenDeleteOnClose OpenFlags = sqlite3.SQLITE_OPEN_DELETEONCLOSE
	OpenExclusive     OpenFlags = sqlite3.SQLITE_OPEN_EXCLUSIVE
	OpenMainDB        OpenFlags = sqlite3.SQLITE_OPEN_MAIN_DB
	OpenMainJournal   OpenFlags = sqlite3.SQLITE_OPEN_MAIN_JOURNAL
	OpenTempDB        OpenFlags = sqlite3.SQLITE_OPEN_TEMP_DB
	OpenTempJournal   OpenFlags = sqlite3.SQLITE_OPEN_TEMP_JOURNAL
	OpenTransientDB   OpenFlags = sqlite3.SQLITE_OPEN_TRANSIENT_DB
	OpenSubJournal    OpenFlags = sqlite3.SQLITE_OPEN_SUBJOURNAL
	OpenWAL           OpenFlags = sqlite3.SQLITE_OPEN_WAL
)

// Has reports whether every bit in f is set in fl.
func (fl OpenFlags) Has(f OpenFlags) bool { return fl&f == f }

// AccessOp selects what VFS.Access checks.
type AccessOp int

const (
	AccessExists    AccessOp = sqlite3.SQLITE_ACCESS_EXISTS
	AccessReadWrite AccessOp = sqlite3.SQLITE_ACCESS_READWRITE
	AccessRead      AccessOp = sqlite3.SQLITE_ACCESS_READ
)

// LockLevel is an advisory lock state, lowest to highest.
type LockLevel int

const (
	LockNone      LockLevel = sqlite3.SQLITE_LOCK_NONE
	LockShared    LockLevel = sqlite3.SQLITE_LOCK_SHARED
	LockReserved  LockLevel = sqlite3.SQLITE_LOCK_RESERVED
	LockPending   LockLevel = sqlite3.SQLITE_LOCK_PENDING
	LockExclusive LockLevel = sqlite3.SQLITE_LOCK_EXCLUSIVE
)

// SyncFlags is the sync level passed to File.Sync.
type SyncFlags int

const (
	SyncNormal   SyncFlags = sqlite3.SQLITE_SYNC_NORMAL
	SyncFull     SyncFlags = sqlite3.SQLITE_SYNC_FULL
	SyncDataOnly SyncFlags = sqlite3.SQLITE_SYNC_DATAONLY
)

// DeviceFlags is the IOCAP_* bitset File.DeviceCharacteristics returns.
type DeviceFlags int

const (
	IOCapAtomic             DeviceFlags = sqlite3.SQLITE_IOCAP_ATOMIC
	IOCapSafeAppend         DeviceFlags = sqlite3.SQLITE_IOCAP_SAFE_APPEND
	IOCapSequential         DeviceFlags = sqlite3.SQLITE_IOCAP_SEQUENTIAL
	IOCapPowersafeOverwrite DeviceFlags = sqlite3.SQLITE_IOCAP_POWERSAFE_OVERWRITE
	IOCapImmutable          DeviceFlags = sqlite3.SQLITE_IOCAP_IMMUTABLE
)

// NoLock is an embeddable helper supplying accept-everything advisory
// locking — the correct behaviour for any single-process or
// exclusive-access File. Embed it to drop three methods of boilerplate:
//
//	type myFile struct {
//		vfs.NoLock
//		// … your fields …
//	}
type NoLock struct{}

// Lock accepts every level (no-op).
func (NoLock) Lock(LockLevel) error { return nil }

// Unlock accepts every level (no-op).
func (NoLock) Unlock(LockLevel) error { return nil }

// CheckReservedLock always reports no reserved lock.
func (NoLock) CheckReservedLock() (bool, error) { return false, nil }

// Option configures a [Register] call.
type Option func(*options)

type options struct {
	makeDefault bool
	maxPathname int
}

// WithMakeDefault registers the VFS as the process default, so a DSN
// without an explicit ?vfs= picks it. Use sparingly — it changes the
// default for every connection opened afterwards.
func WithMakeDefault() Option { return func(o *options) { o.makeDefault = true } }

// WithMaxPathname caps the path length SQLite will hand to the VFS
// (bytes). Defaults to the platform VFS's limit, which is plenty for
// most backends.
func WithMaxPathname(n int) Option { return func(o *options) { o.maxPathname = n } }
