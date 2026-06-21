package compress

// mainfile.go is the compressing main-database storage engine. State is split
// in two:
//
//   - container: the shared, refcounted in-memory state for one database file —
//     the page directory, block allocator, superblock metadata, and the backing
//     *os.File. Every connection that opens the same canonical path shares one
//     container (see the registry in live.go), so they all observe the same
//     committed state with no disk re-read.
//   - mainFile: a per-connection handle over a container, carrying only this
//     connection's advisory lock level.
//
// SQLite's advisory-lock protocol (implemented in-process below, like the
// reference VFS) provides the logical mutual exclusion — many SHARED readers,
// one RESERVED..EXCLUSIVE writer, and EXCLUSIVE excludes all readers — so the
// single writer never overlaps a reader. The container's RWMutex guards the
// in-memory structures for memory-safety. Because only one writer runs at a
// time and no reader overlaps a commit, copy-on-write allocation and the
// superblock flip never race.

import (
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sync"

	"gosqlite.org/vfs"
)

// codecAZ is the superblock codec tag for az-compressed slots (the only codec
// in Phase 1; 0 is reserved for an all-raw container).
const codecAZ uint8 = 1

var (
	errReadOnly = errors.New("compress: write to a read-only database")
	errLockBusy = vfs.Errno(5) // SQLITE_BUSY
)

// backing is the physical block store behind a container. A real *os.File
// satisfies it via fileBacking (live.go); tests substitute an in-memory,
// fault-injecting backing to prove the commit protocol survives a crash at any
// step. The interface is exactly what the storage engine needs — block I/O,
// fsync, size, close — and nothing more.
type backing interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Size() (int64, error)
	Close() error
}

// container is the shared in-memory state for one open database file.
type container struct {
	// mu guards the storage-engine state below (directory, allocator,
	// superblock metadata). RLock for reads, Lock for writes/commit.
	mu   sync.RWMutex
	back backing // physical block-structured container

	blockSize uint64      // physical block granularity B
	pageSize  uint64      // logical SQLite page size
	codec     Compression // level for NEW page writes

	pageCount uint64     // logical page count (authoritative size source)
	dir       []dirEntry // page directory, index = logical page number
	alloc     *allocator // physical block allocator

	committedGen       uint64 // generation of the on-disk authoritative superblock
	committedDirOffset uint64 // its directory extent (released after the next commit)
	committedDirBlocks uint32
	nextSlot           int64 // physical block (0 or 1) the next superblock write targets

	pendingRelease []extent // extents superseded since the last commit; freed only after the next durable commit
	dirty          bool     // logical writes since the last commit
	readOnly       bool

	// Advisory-lock state, shared by every handle on this container. Guarded by
	// lmu, independent of mu. Mirrors the reference File: many SHARED holders,
	// one RESERVED..EXCLUSIVE writer; EXCLUSIVE additionally requires no other
	// connection holds SHARED.
	lmu     sync.Mutex
	nShared int
	writer  *mainFile

	// Registry bookkeeping (guarded by the registry mutex in live.go). name is
	// the canonical path, or "" for an unshared container (tests / anonymous).
	name string
	refs int
}

// mainFile is one connection's handle over a shared container.
type mainFile struct {
	c    *container
	lock vfs.LockLevel
}

// --- vfs.File: I/O (delegates to the shared container under its RWMutex) ---

func (f *mainFile) Size() (int64, error)                   { return f.c.size() }
func (f *mainFile) SectorSize() int                        { return int(f.c.blockSize) }
func (f *mainFile) DeviceCharacteristics() vfs.DeviceFlags { return 0 }

func (f *mainFile) ReadAt(p []byte, off int64) (int, error)  { return f.c.readAt(p, off) }
func (f *mainFile) WriteAt(p []byte, off int64) (int, error) { return f.c.writeAt(p, off) }
func (f *mainFile) Truncate(size int64) error                { return f.c.truncate(size) }
func (f *mainFile) Sync(vfs.SyncFlags) error                 { return f.c.sync() }

// Close drops this connection's advisory lock and releases the container
// (closing the backing and unregistering it when the last handle goes away).
// Buffered-but-unsynced writes are intentionally NOT committed: only Sync'd
// data is durable, and the orphaned slots are reclaimed by the next open.
func (f *mainFile) Close() error {
	_ = f.Unlock(vfs.LockNone)
	return f.c.release()
}

func (c *container) size() (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return int64(c.pageSize * c.pageCount), nil
}

// readAt serves a logical, page-aligned read by decompressing the slots it
// spans. A read at or past the logical end returns io.EOF with a short count,
// so the dispatcher zero-fills and reports SQLITE_IOERR_SHORT_READ.
func (c *container) readAt(p []byte, off int64) (int, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	logical := int64(c.pageSize * c.pageCount)
	read := 0
	for read < len(p) {
		cur := off + int64(read)
		if cur >= logical {
			return read, io.EOF
		}
		pageNo := uint64(cur) / c.pageSize
		intra := uint64(cur) % c.pageSize
		page, err := c.loadPage(pageNo)
		if err != nil {
			return read, err
		}
		read += copy(p[read:], page[intra:])
	}
	return read, nil
}

// writeAt stores a logical, page-aligned write. SQLite writes whole pages, but
// a partial write (e.g. the 100-byte header alone) is handled by
// read-modify-write so the rest of the page is preserved.
func (c *container) writeAt(p []byte, off int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readOnly {
		return 0, errReadOnly
	}
	written := 0
	for written < len(p) {
		cur := off + int64(written)
		pageNo := uint64(cur) / c.pageSize
		intra := uint64(cur) % c.pageSize
		n := min(int(c.pageSize-intra), len(p)-written)

		var page []byte
		if intra == 0 && n == int(c.pageSize) {
			page = make([]byte, c.pageSize)
			copy(page, p[written:written+n])
		} else {
			var err error
			if page, err = c.loadPage(pageNo); err != nil {
				return written, err
			}
			copy(page[intra:], p[written:written+n])
		}
		if err := c.storePage(pageNo, page); err != nil {
			return written, err
		}
		if pageNo+1 > c.pageCount {
			c.pageCount = pageNo + 1
		}
		written += n
	}
	c.dirty = true
	return written, nil
}

// truncate resizes the logical database. Slots above the new page count are
// scheduled for release at the next commit; the physical file is not shrunk
// here (reclaimed space is reused by the allocator, and fully reclaimed on the
// next open's directory scan).
func (c *container) truncate(size int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readOnly {
		return errReadOnly
	}
	newCount := uint64(size) / c.pageSize
	if uint64(size)%c.pageSize != 0 {
		newCount++ // defensive: SQLite truncates on page boundaries
	}
	for i := newCount; i < uint64(len(c.dir)); i++ {
		if e := c.dir[i]; e.physOffset != 0 {
			c.pendingRelease = append(c.pendingRelease, extent{start: e.physOffset / c.blockSize, count: uint64(e.blocks)})
		}
	}
	if newCount < uint64(len(c.dir)) {
		c.dir = c.dir[:newCount]
	}
	c.pageCount = newCount
	c.dirty = true
	return nil
}

// sync is the commit point: it makes every write since the last Sync durable
// and atomic. SQLite calls it once per transaction in rollback-journal mode,
// after writing all dirty pages and before deleting the journal.
func (c *container) sync() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readOnly {
		return nil
	}
	if !c.dirty {
		return c.back.Sync()
	}
	return c.commit()
}

// loadPage returns the pageSize-byte content of a logical page, zero-filled if
// the page is sparse (never written). The caller holds c.mu (R or W).
func (c *container) loadPage(pageNo uint64) ([]byte, error) {
	buf := make([]byte, c.pageSize)
	if pageNo >= uint64(len(c.dir)) {
		return buf, nil
	}
	e := c.dir[pageNo]
	if e.physOffset == 0 {
		return buf, nil
	}
	stored := make([]byte, e.storedLen)
	if _, err := c.back.ReadAt(stored, int64(e.physOffset)); err != nil {
		return nil, fmt.Errorf("compress: read page %d slot: %w", pageNo, err)
	}
	if crc32.Checksum(stored, crc32C) != e.checksum {
		return nil, fmt.Errorf("compress: page %d slot checksum mismatch (corruption)", pageNo)
	}
	if e.flags&dirFlagVerbatim != 0 {
		copy(buf, stored)
		return buf, nil
	}
	if err := decodePage(buf, stored); err != nil {
		return nil, fmt.Errorf("compress: decode page %d: %w", pageNo, err)
	}
	return buf, nil
}

// storePage compresses a full page, COW-allocates a fresh block run for it, and
// updates the in-memory directory. The page's previous slot is scheduled for
// release at the next durable commit — never overwritten in place. The caller
// holds c.mu for writing.
func (c *container) storePage(pageNo uint64, page []byte) error {
	stored, verbatim, err := encodePage(page, c.codec)
	if err != nil {
		return err
	}
	nb := blocksFor(uint64(len(stored)), c.blockSize)
	physOffset := c.alloc.alloc(nb) * c.blockSize
	if err := c.writeBlocks(stored, physOffset, nb); err != nil {
		return err
	}

	c.growDir(pageNo)
	if old := c.dir[pageNo]; old.physOffset != 0 {
		c.pendingRelease = append(c.pendingRelease, extent{start: old.physOffset / c.blockSize, count: uint64(old.blocks)})
	}
	var flags uint16
	if verbatim {
		flags = dirFlagVerbatim
	}
	c.dir[pageNo] = dirEntry{
		physOffset: physOffset,
		storedLen:  uint32(len(stored)),
		blocks:     uint32(nb),
		flags:      flags,
		checksum:   crc32.Checksum(stored, crc32C),
	}
	return nil
}

// growDir extends the directory so index pageNo exists, filling gaps with
// sparse entries.
func (c *container) growDir(pageNo uint64) {
	for uint64(len(c.dir)) <= pageNo {
		c.dir = append(c.dir, dirEntry{})
	}
}

// writeBlocks writes data to a block run, zero-padding the final block so the
// physical file length stays a whole number of blocks.
func (c *container) writeBlocks(data []byte, physOffset, blocks uint64) error {
	buf := make([]byte, blocks*c.blockSize)
	copy(buf, data)
	_, err := c.back.WriteAt(buf, int64(physOffset))
	return err
}

// commit runs the crash-safe commit protocol synchronously (caller holds c.mu
// for writing):
//
//  1. (data slots are already written by storePage)
//  2. write the new directory to fresh blocks (COW — never over the live copy)
//  3. fsync: slots + directory durable
//  4. write the ALTERNATE superblock with generation+1, pointing at the new dir
//  5. fsync: the superblock flip is durable — generation+1 is now authoritative
//  6. release the superseded extents (prior directory + this txn's old slots),
//     now safe to reuse since the generation that vacated them is durable
//
// A crash before step 5 completes leaves the older generation as the
// highest-valid superblock, so reopen reconstructs the previous committed
// state; SQLite's rollback journal then recovers the logical transaction.
func (c *container) commit() error {
	var dirOffset uint64
	var dirBlocks uint32
	var dirChecksum uint32
	if len(c.dir) > 0 {
		dirBytes := marshalDirectory(c.dir)
		dirChecksum = crc32.Checksum(dirBytes, crc32C)
		nb := blocksFor(uint64(len(dirBytes)), c.blockSize)
		dirOffset = c.alloc.alloc(nb) * c.blockSize
		dirBlocks = uint32(nb)
		if err := c.writeBlocks(dirBytes, dirOffset, nb); err != nil {
			return err
		}
	}
	if err := c.back.Sync(); err != nil {
		return err
	}

	newGen := c.committedGen + 1
	sb := &superblock{
		blockSize:   uint32(c.blockSize),
		pageSize:    uint32(c.pageSize),
		pageCount:   c.pageCount,
		dirOffset:   dirOffset,
		dirBlocks:   dirBlocks,
		generation:  newGen,
		codec:       codecAZ,
		dirChecksum: dirChecksum,
	}
	if _, err := c.back.WriteAt(sb.marshal(), c.nextSlot*int64(c.blockSize)); err != nil {
		return err
	}
	if err := c.back.Sync(); err != nil {
		return err
	}

	if c.committedDirBlocks > 0 {
		c.pendingRelease = append(c.pendingRelease, extent{start: c.committedDirOffset / c.blockSize, count: uint64(c.committedDirBlocks)})
	}
	for _, e := range c.pendingRelease {
		c.alloc.release(e.start, e.count)
	}
	c.pendingRelease = c.pendingRelease[:0]
	c.committedGen = newGen
	c.committedDirOffset = dirOffset
	c.committedDirBlocks = dirBlocks
	c.nextSlot = 1 - c.nextSlot
	c.dirty = false
	return nil
}

// --- vfs.File: in-process advisory locking (mirrors the reference File) ---

// Lock raises this connection's advisory lock toward level, arbitrating in
// process against the other connections on the same container. Many holders may
// share SHARED; only one may hold RESERVED..EXCLUSIVE; EXCLUSIVE additionally
// requires that no other connection holds SHARED.
func (f *mainFile) Lock(level vfs.LockLevel) error {
	if level <= f.lock {
		return nil
	}
	c := f.c
	c.lmu.Lock()
	defer c.lmu.Unlock()
	switch level {
	case vfs.LockShared:
		if c.writer != nil && c.writer.lock >= vfs.LockPending {
			return errLockBusy // a PENDING/EXCLUSIVE writer blocks new readers
		}
		c.nShared++
		f.lock = vfs.LockShared
	case vfs.LockReserved:
		if c.writer != nil && c.writer != f {
			return errLockBusy
		}
		c.writer = f
		f.lock = vfs.LockReserved
	case vfs.LockPending, vfs.LockExclusive:
		if c.writer != nil && c.writer != f {
			return errLockBusy
		}
		c.writer = f
		self := 0
		if f.lock >= vfs.LockShared {
			self = 1
		}
		if c.nShared > self {
			f.lock = vfs.LockPending // hold the intent so no new SHARED is granted
			return errLockBusy
		}
		f.lock = level
	}
	return nil
}

// Unlock lowers this connection's advisory lock toward level.
func (f *mainFile) Unlock(level vfs.LockLevel) error {
	if level >= f.lock {
		return nil
	}
	c := f.c
	c.lmu.Lock()
	defer c.lmu.Unlock()
	if f.lock >= vfs.LockReserved && level < vfs.LockReserved && c.writer == f {
		c.writer = nil
	}
	if f.lock >= vfs.LockShared && level < vfs.LockShared {
		c.nShared--
	}
	f.lock = level
	return nil
}

// CheckReservedLock reports whether some connection holds RESERVED or higher.
func (f *mainFile) CheckReservedLock() (bool, error) {
	c := f.c
	c.lmu.Lock()
	defer c.lmu.Unlock()
	return c.writer != nil && c.writer.lock >= vfs.LockReserved, nil
}
