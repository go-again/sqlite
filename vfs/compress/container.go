package compress

// container.go is the compressing main-database storage engine (the on-disk
// wire format it reads and writes is in format.go). State is split in two:
//
//   - container: the shared, refcounted in-memory state for one database file —
//     the page directory, block allocator, superblock metadata, and the backing
//     *os.File. Every connection that opens the same canonical path shares one
//     container (see the registry in vfs.go), so they all observe the same
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
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sync"

	"gosqlite.org/crypto/keyring"
	"gosqlite.org/vfs"
	"gosqlite.org/vfs/crypto"
)

// codecAZ is the superblock codec tag for az-compressed slots (the only codec
// in Phase 1; 0 is reserved for an all-raw container).
const codecAZ uint8 = 1

var (
	errReadOnly = errors.New("compress: write to a read-only database")
	errLockBusy = vfs.Errno(5) // SQLITE_BUSY
)

// extentPool reuses the block-aligned scratch buffer the encrypted slot path
// needs — decrypt-in-place on read, encrypt-in-place on write. On the plaintext
// path the bytes go straight to/from the slot, so only encryption pays this
// per-page allocation (a page-sized buffer per I/O); pooling keeps it off the
// GC's hot path, mirroring vfs/crypto's scratchPool. Pooled by capacity, since
// most slots are about one page. Callers must zero any padding past the stored
// prefix before writing — a pooled buffer is not pre-cleared.
var extentPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, defaultPageSize)
		return &b
	},
}

func getExtent(n uint64) *[]byte {
	bp := extentPool.Get().(*[]byte)
	if uint64(cap(*bp)) < n {
		extentPool.Put(bp) // too small: return it rather than drop it, then allocate exact
		buf := make([]byte, n)
		return &buf
	}
	*bp = (*bp)[:n]
	return bp
}

func putExtent(bp *[]byte) {
	*bp = (*bp)[:0]
	extentPool.Put(bp)
}

// backing is the physical block store behind a container. A real *os.File
// satisfies it via fileBacking (vfs.go); tests substitute an in-memory,
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

	// cipher encrypts each block-aligned slot extent at rest (nil = plaintext);
	// enc is the matching superblock marker written on commit. Set once at open
	// and shared, read-only, by every handle — the cipher is concurrency-safe.
	// keyslotOffset is the physical offset of the wrapped-data-key block (0 =
	// none, i.e. raw key or unencrypted), carried into each committed superblock.
	// dek is the in-memory data key the cipher was built from, retained so the
	// at-rest key-management operations (Rewrap/Rekey) can re-wrap it and so a
	// second opener's key/identity can be validated against the live container
	// (matchesKeyConfig); it is the same secret the cipher already holds in
	// derived form. keyslotBlob caches the wrapped data key (recipients mode) for
	// that same validation, so a registry hit need not re-read it from disk.
	cipher        crypto.PageCipher
	enc           uint8
	keyslotOffset uint64
	dek           []byte
	keyslotBlob   []byte

	// Authenticated mode (writer-signed container): commits are signed with
	// writeAs and verified against writers (the keyslot's authorized writer set);
	// each slot carries a crypto hash. A container that is authenticated but has
	// no writeAs is read-only (it cannot produce a signed commit).
	authenticated     bool
	writeAs           keyring.WriterIdentity
	writers           []ed25519.PublicKey
	readOnlyRecipient bool // authenticated but no writer identity: writes are refused

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
	// lmu, independent of mu. Many SHARED holders, one RESERVED..EXCLUSIVE
	// writer; EXCLUSIVE additionally requires no other connection holds SHARED.
	//
	// The Lock/Unlock/CheckReservedLock state machine is a deliberate port of
	// the reference File in gosqlite.org/vfs/interface_test.go (refMemFile) —
	// kept byte-identical so the two cannot silently drift. It is duplicated
	// rather than shared only because the reference lives in a _test.go (not
	// importable) and this is an isolated module; promoting it to a public vfs
	// helper both embed is the real fix (tracked separately).
	lmu     sync.Mutex
	nShared int
	writer  *mainFile

	// Registry bookkeeping (guarded by the registry mutex in vfs.go). name is
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
//
// Unlock(LockNone) MUST run before release: c.writer is a back-pointer to the
// handle holding RESERVED+, and only that handle's own Unlock clears it.
// Unlocking first guarantees the pointer is cleared before the handle can go
// away, so a surviving connection never dereferences a freed handle.
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
		// Fast path: an aligned, full-page read (the overwhelmingly common case)
		// decodes straight into the caller's buffer — no per-page allocation, no
		// copy. The logical size is always a whole number of pages, so a full
		// page here never runs past EOF.
		if intra == 0 && uint64(len(p)-read) >= c.pageSize {
			if err := c.loadPageInto(p[read:read+int(c.pageSize)], pageNo); err != nil {
				return read, err
			}
			read += int(c.pageSize)
			continue
		}
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
	if c.readOnlyRecipient {
		return 0, ErrReadOnlyRecipient
	}
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
	if c.readOnlyRecipient {
		return ErrReadOnlyRecipient
	}
	if c.readOnly {
		return errReadOnly
	}
	newCount := uint64(size) / c.pageSize
	if uint64(size)%c.pageSize != 0 {
		newCount++ // defensive: SQLite truncates on page boundaries
	}
	for i := newCount; i < uint64(len(c.dir)); i++ {
		if e := c.dir[i]; e.physOffset != 0 {
			c.releaseLater(e.physOffset, e.blocks)
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

// loadPage returns a freshly allocated pageSize buffer holding a logical page.
// Prefer loadPageInto on the hot path to decode straight into the destination.
func (c *container) loadPage(pageNo uint64) ([]byte, error) {
	buf := make([]byte, c.pageSize)
	if err := c.loadPageInto(buf, pageNo); err != nil {
		return nil, err
	}
	return buf, nil
}

// slotHash is the per-slot authentication tag used in authenticated mode: a
// crypto hash of the on-disk slot bytes (ciphertext), truncated to slotHashLen.
// The directory that records it is writer-signed, so this binds each slot's
// content cryptographically — a CRC32 alone is collision-prone.
func slotHash(onDisk []byte) [slotHashLen]byte {
	sum := sha256.Sum256(onDisk)
	var h [slotHashLen]byte
	copy(h[:], sum[:slotHashLen])
	return h
}

// readVerifyDecrypt enforces the encrypted-unit read invariant in one place: the
// checksum is taken over the on-disk CIPHERTEXT (so corruption is caught before
// the cipher runs on hostile bytes), then the buffer is decrypted in place. It
// reports whether the checksum matched; the caller forms its own context error.
// Shared by the data-slot (loadPageInto) and directory (newContainerOver) reads.
func (c *container) readVerifyDecrypt(buf []byte, checksum uint32, tweak uint64, domain byte) bool {
	if crc32.Checksum(buf, crc32C) != checksum {
		return false
	}
	c.cipher.Decrypt(buf, tweak, domain)
	return true
}

// loadPageInto fills dst (which must be exactly pageSize) with a logical page,
// zero-filling if the page is sparse (never written). It verifies the slot
// checksum and bounds decompression to the page size. The caller holds c.mu
// (R or W).
func (c *container) loadPageInto(dst []byte, pageNo uint64) error {
	if pageNo >= uint64(len(c.dir)) {
		clear(dst)
		return nil
	}
	e := c.dir[pageNo]
	if e.physOffset == 0 {
		clear(dst)
		return nil
	}
	var stored []byte
	if c.cipher != nil {
		// The cipher is a wide-block transform over the whole slot extent, so
		// read, checksum (the on-disk ciphertext), and decrypt the full
		// block-aligned run, then take the stored (compressed) prefix. The extent
		// is pooled and consumed (decoded into dst) before this function returns.
		bp := getExtent(uint64(e.blocks) * c.blockSize)
		defer putExtent(bp)
		extent := *bp
		if _, err := c.back.ReadAt(extent, int64(e.physOffset)); err != nil {
			return fmt.Errorf("compress: read page %d slot: %w", pageNo, err)
		}
		if c.authenticated && slotHash(extent) != e.hash {
			return fmt.Errorf("compress: page %d slot authentication failed (tampered)", pageNo)
		}
		if !c.readVerifyDecrypt(extent, e.checksum, pageNo, domainPageData) {
			return fmt.Errorf("compress: page %d slot checksum mismatch (corruption)", pageNo)
		}
		stored = extent[:e.storedLen]
	} else {
		stored = make([]byte, e.storedLen)
		if _, err := c.back.ReadAt(stored, int64(e.physOffset)); err != nil {
			return fmt.Errorf("compress: read page %d slot: %w", pageNo, err)
		}
		if crc32.Checksum(stored, crc32C) != e.checksum {
			return fmt.Errorf("compress: page %d slot checksum mismatch (corruption)", pageNo)
		}
	}
	if e.flags&dirFlagVerbatim != 0 {
		copy(dst, stored) // verbatim slot is exactly pageSize
		return nil
	}
	if err := decodePage(dst, stored); err != nil {
		return fmt.Errorf("compress: decode page %d: %w", pageNo, err)
	}
	return nil
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

	// Build the on-disk extent (zero-padded to a whole block run). When
	// encrypting, the cipher transforms the whole extent in place — always >= one
	// block, so the wide-block cipher's minimum input is met — and the checksum
	// covers the ciphertext; plaintext keeps the historical scope (stored prefix).
	// The buffer is pooled, so the padding past stored is zeroed explicitly to
	// keep the on-disk bytes deterministic (and free of any prior page's data).
	bp := getExtent(nb * c.blockSize)
	defer putExtent(bp)
	extent := *bp
	copy(extent, stored)
	clear(extent[len(stored):])
	checksum := crc32.Checksum(stored, crc32C)
	if c.cipher != nil {
		c.cipher.Encrypt(extent, pageNo, domainPageData)
		checksum = crc32.Checksum(extent, crc32C)
	}
	var hash [slotHashLen]byte
	if c.authenticated {
		hash = slotHash(extent)
	}
	if _, err := c.back.WriteAt(extent, int64(physOffset)); err != nil {
		return err
	}

	c.growDir(pageNo)
	if old := c.dir[pageNo]; old.physOffset != 0 {
		c.releaseLater(old.physOffset, old.blocks)
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
		checksum:   checksum,
		hash:       hash,
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

// releaseLater schedules a physical extent (a byte offset plus a block count)
// for return to the allocator at the next durable commit. Centralizing the
// byte→block conversion keeps the single off-by-blockSize that would corrupt
// the free list in one place; the deferral is the crash-safety invariant —
// superseded blocks are reusable only once the commit that vacated them is
// durable (see commit).
func (c *container) releaseLater(physOffset uint64, blocks uint32) {
	c.pendingRelease = append(c.pendingRelease, extent{start: physOffset / c.blockSize, count: uint64(blocks)})
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
//
// Tradeoffs (intentional, do not "optimize" away):
//   - Two fsyncs are the required minimum for this COW + ping-pong protocol —
//     slots+directory must be durable before the superblock flip is durable.
//     Collapsing to one fsync would break atomicity.
//   - The whole directory is re-marshalled and rewritten every commit (its size
//     is O(pageCount)), even for a one-page change. This is fine up to a few
//     hundred MB; for large databases with a high small-transaction rate it is
//     the main scaling cost, and an incremental/segmented directory is a
//     deliberate future format change.
//   - commit holds c.mu (write lock) across both fsyncs, so a reader stalls for
//     the commit window. Correct (no reader overlaps a writer), but the
//     critical section should be shrunk before optimizing for heavy multi-reader
//     concurrency.
func (c *container) commit() error {
	var dirOffset uint64
	var dirBlocks uint32
	var dirChecksum uint32
	var dirHash [dirHashLen]byte
	// An encrypted container always writes a directory (at least the canary) so a
	// wrong key is caught at open even on an empty database.
	if len(c.dir) > 0 || c.cipher != nil {
		entries := marshalDirectory(c.dir, c.authenticated)
		if c.cipher != nil {
			// [canary || entries], zero-padded to a block run and encrypted in
			// place; the checksum covers the on-disk ciphertext.
			nb := blocksFor(uint64(dirCanaryLen+len(entries)), c.blockSize)
			buf := make([]byte, nb*c.blockSize)
			copy(buf, dirCanary[:])
			copy(buf[dirCanaryLen:], entries)
			c.cipher.Encrypt(buf, dirTweak, domainDirectory)
			dirOffset = c.alloc.alloc(nb) * c.blockSize
			dirBlocks = uint32(nb)
			dirChecksum = crc32.Checksum(buf, crc32C)
			if c.authenticated {
				dirHash = sha256.Sum256(buf) // crypto hash of the on-disk directory, signed below
			}
			if _, err := c.back.WriteAt(buf, int64(dirOffset)); err != nil {
				return err
			}
		} else {
			dirChecksum = crc32.Checksum(entries, crc32C)
			nb := blocksFor(uint64(len(entries)), c.blockSize)
			dirOffset = c.alloc.alloc(nb) * c.blockSize
			dirBlocks = uint32(nb)
			if err := c.writeBlocks(entries, dirOffset, nb); err != nil {
				return err
			}
		}
	}
	if err := c.back.Sync(); err != nil {
		return err
	}

	newGen := c.committedGen + 1
	sb := &superblock{
		blockSize:     uint32(c.blockSize),
		pageSize:      uint32(c.pageSize),
		pageCount:     c.pageCount,
		dirOffset:     dirOffset,
		dirBlocks:     dirBlocks,
		generation:    newGen,
		codec:         codecAZ,
		enc:           c.enc,
		dirChecksum:   dirChecksum,
		keyslotOffset: c.keyslotOffset,
		authenticated: c.authenticated,
		dirHash:       dirHash,
	}
	if c.authenticated {
		// Sign the committed state (base fields + directory hash) as a writer; a
		// container with no writer identity is read-only and never reaches commit.
		if c.writeAs == nil {
			return errReadOnly
		}
		copy(sb.writerSig[:], keyring.SignState(c.writeAs, sb.signedState()))
	}
	if _, err := c.back.WriteAt(sb.marshal(), c.nextSlot*int64(c.blockSize)); err != nil {
		return err
	}
	if err := c.back.Sync(); err != nil {
		return err
	}

	if c.committedDirBlocks > 0 {
		c.releaseLater(c.committedDirOffset, c.committedDirBlocks)
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

// ShmGroup implements vfs.ShmFile to unlock WAL: it returns the container's
// canonical path, so the dispatcher hands every connection on the same database
// one shared WAL index. The shared-memory regions and WAL lock table are the
// dispatcher's; this is all a File must declare. (An unshared container —
// tests / anonymous — returns "", a private group it never actually uses, since
// those handles are driven directly and never enter WAL.)
func (f *mainFile) ShmGroup() string { return f.c.name }

var _ vfs.ShmFile = (*mainFile)(nil)
