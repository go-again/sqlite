package compress

// mainfile.go is the compressing main-database File of the live VFS: it
// translates SQLite's logical, page-aligned reads and writes into compressed,
// block-aligned slots in the container (container.go), and implements the
// crash-safe commit protocol on Sync. It backs onto a plain *os.File; the whole
// storage engine is ordinary Go, which is what makes the commit path
// crash-injectable (Inc 3). Journals and temp files do NOT come here — the VFS
// routes them to a pass-through File (live.go).

import (
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"gosqlite.org/vfs"
)

// codecAZ is the superblock codec tag for az-compressed slots (the only codec
// in Phase 1; 0 is reserved for an all-raw container).
const codecAZ uint8 = 1

var errReadOnly = errors.New("compress: write to a read-only database")

// backing is the physical block store behind a mainFile. A real *os.File
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

// mainFile is one open compressed main database. It is reached by a single
// connection at a time (the live VFS forces MaxOpenConns(1)), so it embeds
// vfs.NoLock and needs no internal synchronisation.
type mainFile struct {
	vfs.NoLock
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
}

// Size reports the logical database size, derived from the page count — never
// the physical (compressed) file length.
func (f *mainFile) Size() (int64, error) { return int64(f.pageSize * f.pageCount), nil }

// SectorSize reports the physical block size; DeviceCharacteristics advertises
// no special guarantees, so SQLite keeps full journal discipline.
func (f *mainFile) SectorSize() int                        { return int(f.blockSize) }
func (f *mainFile) DeviceCharacteristics() vfs.DeviceFlags { return 0 }

// ReadAt serves a logical, page-aligned read by decompressing the slots it
// spans. A read at or past the logical end returns io.EOF with a short count,
// so the dispatcher zero-fills and reports SQLITE_IOERR_SHORT_READ — exactly
// like the native VFS on a fresh or truncated database.
func (f *mainFile) ReadAt(p []byte, off int64) (int, error) {
	logical := int64(f.pageSize * f.pageCount)
	read := 0
	for read < len(p) {
		cur := off + int64(read)
		if cur >= logical {
			return read, io.EOF
		}
		pageNo := uint64(cur) / f.pageSize
		intra := uint64(cur) % f.pageSize
		page, err := f.loadPage(pageNo)
		if err != nil {
			return read, err
		}
		read += copy(p[read:], page[intra:])
	}
	return read, nil
}

// WriteAt stores a logical, page-aligned write. SQLite writes whole pages, but
// a partial write (e.g. the 100-byte header alone) is handled by
// read-modify-write so the rest of the page is preserved.
func (f *mainFile) WriteAt(p []byte, off int64) (int, error) {
	if f.readOnly {
		return 0, errReadOnly
	}
	written := 0
	for written < len(p) {
		cur := off + int64(written)
		pageNo := uint64(cur) / f.pageSize
		intra := uint64(cur) % f.pageSize
		n := min(int(f.pageSize-intra), len(p)-written)

		var page []byte
		if intra == 0 && n == int(f.pageSize) {
			page = make([]byte, f.pageSize)
			copy(page, p[written:written+n])
		} else {
			var err error
			if page, err = f.loadPage(pageNo); err != nil {
				return written, err
			}
			copy(page[intra:], p[written:written+n])
		}
		if err := f.storePage(pageNo, page); err != nil {
			return written, err
		}
		if pageNo+1 > f.pageCount {
			f.pageCount = pageNo + 1
		}
		written += n
	}
	f.dirty = true
	return written, nil
}

// Truncate resizes the logical database. Slots above the new page count are
// scheduled for release at the next commit; the physical file is not shrunk
// here (reclaimed space is reused by the allocator, and fully reclaimed on the
// next open's directory scan).
func (f *mainFile) Truncate(size int64) error {
	if f.readOnly {
		return errReadOnly
	}
	newCount := uint64(size) / f.pageSize
	if uint64(size)%f.pageSize != 0 {
		newCount++ // defensive: SQLite truncates on page boundaries
	}
	for i := newCount; i < uint64(len(f.dir)); i++ {
		if e := f.dir[i]; e.physOffset != 0 {
			f.pendingRelease = append(f.pendingRelease, extent{start: e.physOffset / f.blockSize, count: uint64(e.blocks)})
		}
	}
	if newCount < uint64(len(f.dir)) {
		f.dir = f.dir[:newCount]
	}
	f.pageCount = newCount
	f.dirty = true
	return nil
}

// Sync is the commit point: it makes every write since the last Sync durable
// and atomic. SQLite calls it once per transaction in rollback-journal mode,
// after writing all dirty pages and before deleting the journal.
func (f *mainFile) Sync(vfs.SyncFlags) error {
	if f.readOnly {
		return nil
	}
	if !f.dirty {
		return f.back.Sync()
	}
	return f.commit()
}

// Close releases the backing file WITHOUT committing buffered-but-unsynced
// writes: only Sync'd data is durable, so dropping an uncommitted tail is
// correct (SQLite's journal rolls the logical transaction back), and the
// orphaned slots are reclaimed by the next open's directory scan.
func (f *mainFile) Close() error { return f.back.Close() }

// loadPage returns the pageSize-byte content of a logical page, zero-filled if
// the page is sparse (never written). It verifies the slot checksum and bounds
// decompression to the page size.
func (f *mainFile) loadPage(pageNo uint64) ([]byte, error) {
	buf := make([]byte, f.pageSize)
	if pageNo >= uint64(len(f.dir)) {
		return buf, nil
	}
	e := f.dir[pageNo]
	if e.physOffset == 0 {
		return buf, nil
	}
	stored := make([]byte, e.storedLen)
	if _, err := f.back.ReadAt(stored, int64(e.physOffset)); err != nil {
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
// release at the next durable commit — never overwritten in place — so a crash
// before the commit leaves the previous committed state fully intact.
func (f *mainFile) storePage(pageNo uint64, page []byte) error {
	stored, verbatim, err := encodePage(page, f.codec)
	if err != nil {
		return err
	}
	nb := blocksFor(uint64(len(stored)), f.blockSize)
	physOffset := f.alloc.alloc(nb) * f.blockSize
	if err := f.writeBlocks(stored, physOffset, nb); err != nil {
		return err
	}

	f.growDir(pageNo)
	if old := f.dir[pageNo]; old.physOffset != 0 {
		f.pendingRelease = append(f.pendingRelease, extent{start: old.physOffset / f.blockSize, count: uint64(old.blocks)})
	}
	var flags uint16
	if verbatim {
		flags = dirFlagVerbatim
	}
	f.dir[pageNo] = dirEntry{
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
func (f *mainFile) growDir(pageNo uint64) {
	for uint64(len(f.dir)) <= pageNo {
		f.dir = append(f.dir, dirEntry{})
	}
}

// writeBlocks writes data to a block run, zero-padding the final block so the
// physical file length stays a whole number of blocks (which keeps the
// high-water mark unambiguous on the next open).
func (f *mainFile) writeBlocks(data []byte, physOffset, blocks uint64) error {
	buf := make([]byte, blocks*f.blockSize)
	copy(buf, data)
	_, err := f.back.WriteAt(buf, int64(physOffset))
	return err
}

// commit runs the crash-safe commit protocol synchronously:
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
func (f *mainFile) commit() error {
	var dirOffset uint64
	var dirBlocks uint32
	var dirChecksum uint32
	if len(f.dir) > 0 {
		dirBytes := marshalDirectory(f.dir)
		dirChecksum = crc32.Checksum(dirBytes, crc32C)
		nb := blocksFor(uint64(len(dirBytes)), f.blockSize)
		dirOffset = f.alloc.alloc(nb) * f.blockSize
		dirBlocks = uint32(nb)
		if err := f.writeBlocks(dirBytes, dirOffset, nb); err != nil {
			return err
		}
	}
	if err := f.back.Sync(); err != nil {
		return err
	}

	newGen := f.committedGen + 1
	sb := &superblock{
		blockSize:   uint32(f.blockSize),
		pageSize:    uint32(f.pageSize),
		pageCount:   f.pageCount,
		dirOffset:   dirOffset,
		dirBlocks:   dirBlocks,
		generation:  newGen,
		codec:       codecAZ,
		dirChecksum: dirChecksum,
	}
	if _, err := f.back.WriteAt(sb.marshal(), f.nextSlot*int64(f.blockSize)); err != nil {
		return err
	}
	if err := f.back.Sync(); err != nil {
		return err
	}

	if f.committedDirBlocks > 0 {
		f.pendingRelease = append(f.pendingRelease, extent{start: f.committedDirOffset / f.blockSize, count: uint64(f.committedDirBlocks)})
	}
	for _, e := range f.pendingRelease {
		f.alloc.release(e.start, e.count)
	}
	f.pendingRelease = f.pendingRelease[:0]
	f.committedGen = newGen
	f.committedDirOffset = dirOffset
	f.committedDirBlocks = dirBlocks
	f.nextSlot = 1 - f.nextSlot
	f.dirty = false
	return nil
}
