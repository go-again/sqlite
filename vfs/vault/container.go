package vault

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

// Superblock codec tags: 0 = raw (compression off), 1 = az-compressed slots.
const (
	codecRaw uint8 = 0
	codecAZ  uint8 = 1
)

// codecTag is the superblock codec byte for this container's compression level
// (raw when compression is off, az otherwise).
func (c *container) codecTag() uint8 {
	if c.codec == CompressionNone {
		return codecRaw
	}
	return codecAZ
}

var errReadOnly = errors.New("vault: write to a read-only database")

// extentPool reuses the block-aligned scratch buffer the encrypted slot path
// needs — decrypt-in-place on read, encrypt-in-place on write. On the plaintext
// path the bytes go straight to/from the slot, so only encryption pays this
// per-page allocation (a page-sized buffer per I/O); pooling keeps it off the
// GC's hot path. It is the same idiom as vfs/crypto's scratchPool, kept as a
// separate (~15-line) copy rather than a shared public helper because the two are
// separate modules and the drift risk is benign (a perf regression, not a bug).
// Pooled by capacity, since
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
// fsync, size, truncate, close — and nothing more.
type backing interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Size() (int64, error)
	Truncate(size int64) error // shrink the file to size (online tail-trim)
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
	macKey            []byte // symmetric authenticated mode (no writers): HMAC key for the root proof
	readOnlyRecipient bool   // authenticated but no writer identity: writes are refused

	// anchor, if set, is the external monotonic replay floor: open rejects a
	// committed generation below it, and each commit records the new generation.
	anchor ReplayAnchor

	pageCount uint64     // logical page count (authoritative size source)
	dir       []dirEntry // page directory, index = logical page number
	alloc     *allocator // physical block allocator

	// segEntries is the per-segment directory entry count (geometry, read from the
	// superblock); the segmented commit/open path (containerVersion 2) uses it to
	// rewrite only the directory segments whose pages changed. segIndex mirrors the
	// committed on-disk segment index, so a commit releases the segments it
	// supersedes after the durable flip. dirtySegs holds the segments whose entries
	// changed since the last commit; a commit rewrites those (and the index) and
	// carries the rest forward in place — the O(changed pages) directory write.
	segEntries uint64
	segIndex   []segDesc
	dirtySegs  map[uint64]struct{}

	committedGen       uint64 // generation of the on-disk authoritative superblock
	committedDirOffset uint64 // its directory extent (released after the next commit)
	committedDirBlocks uint32
	nextSlot           int64 // physical block (0 or 1) the next superblock write targets

	pendingRelease []extent // extents superseded since the last commit; freed only after the next durable commit
	dirty          bool     // logical writes since the last commit
	readOnly       bool

	// Advisory-lock state, shared by every handle on this container: SQLite's
	// in-process file-locking protocol (many SHARED holders, one
	// RESERVED..EXCLUSIVE writer), arbitrated by the shared vfs.AdvisoryLock helper.
	alock vfs.AdvisoryLock

	// Registry bookkeeping (guarded by the registry mutex in vfs.go). name is
	// the canonical path, or "" for an unshared container (tests / anonymous).
	name string
	refs int

	// reserved is the canonical path this container holds via reservePath (offline
	// Compact/Rewrap/Rekey); closeContainer releases it. Empty for live containers.
	reserved string
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
// Unlock(LockNone) MUST run before release: the shared alock holds a reference to
// the handle holding RESERVED+, and only that handle's own Unlock clears it.
// Unlocking first guarantees the reference is cleared before the handle can go
// away, so a surviving connection never reads a stale handle.
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
	oldLen := uint64(len(c.dir))
	for i := newCount; i < oldLen; i++ {
		if e := c.dir[i]; e.physOffset != 0 {
			c.releaseLater(e.physOffset, e.blocks)
		}
	}
	if newCount < oldLen {
		c.dir = c.dir[:newCount]
	}
	// Every directory segment from the new last page through the OLD last page
	// changed: the new last segment shrank, and the segments above it were dropped.
	// Mark them all dirty so a regrow later in this SAME transaction recomputes them
	// from c.dir rather than carrying the stale pre-truncate on-disk segment forward
	// — which would resurrect the just-dropped pages as durable, silent corruption.
	if oldLen > 0 && c.segEntries != 0 {
		first := uint64(0)
		if newCount > 0 {
			first = (newCount - 1) / c.segEntries // the (shrunk) new last segment
		}
		for s := first; s*c.segEntries < oldLen; s++ {
			c.markSegmentDirty(s * c.segEntries)
		}
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
			return fmt.Errorf("vault: read page %d slot: %w", pageNo, err)
		}
		if c.authenticated && slotHash(extent) != e.hash {
			return fmt.Errorf("vault: page %d slot authentication failed (tampered)", pageNo)
		}
		if !c.readVerifyDecrypt(extent, e.checksum, pageNo, domainPageData) {
			return fmt.Errorf("vault: page %d slot checksum mismatch (corruption)", pageNo)
		}
		stored = extent[:e.storedLen]
	} else {
		stored = make([]byte, e.storedLen)
		if _, err := c.back.ReadAt(stored, int64(e.physOffset)); err != nil {
			return fmt.Errorf("vault: read page %d slot: %w", pageNo, err)
		}
		if crc32.Checksum(stored, crc32C) != e.checksum {
			return fmt.Errorf("vault: page %d slot checksum mismatch (corruption)", pageNo)
		}
	}
	if e.flags&dirFlagVerbatim != 0 {
		copy(dst, stored) // verbatim slot is exactly pageSize
		return nil
	}
	if err := decodePage(dst, stored); err != nil {
		return fmt.Errorf("vault: decode page %d: %w", pageNo, err)
	}
	return nil
}

// storePage compresses a full page, COW-allocates a fresh block run for it, and
// updates the in-memory directory. The page's previous slot is scheduled for
// release at the next durable commit — never overwritten in place. The caller
// holds c.mu for writing.
func (c *container) storePage(pageNo uint64, page []byte) error {
	// Sparse-on-write: an all-zero page needs no block. Store it sparse — release the
	// previous slot and mark the directory entry empty (it reads back as zeros, so this
	// is content-preserving) — instead of encoding, encrypting, and writing a block.
	// Once a directory segment goes fully sparse the commit drops it too, so a freed
	// region shrinks the directory as well. This is also the "release block + mark slot
	// sparse" primitive the freelist reclaim ([CompactLogicalOnline]) reuses.
	if allZero(page) {
		c.growDir(pageNo)
		if old := c.dir[pageNo]; old.physOffset != 0 {
			c.releaseLater(old.physOffset, old.blocks)
		}
		c.dir[pageNo] = dirEntry{}
		c.markSegmentDirty(pageNo)
		return nil
	}
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
	c.markSegmentDirty(pageNo)
	return nil
}

// growDir extends the directory so index pageNo exists, filling gaps with
// sparse entries.
func (c *container) growDir(pageNo uint64) {
	// The old last segment's page range extends as the directory grows, so its
	// content (entry count) changes — rewrite it, not just pageNo's segment.
	if old := uint64(len(c.dir)); old <= pageNo && old > 0 {
		c.markSegmentDirty(old - 1)
	}
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
	// Persist the directory as segments + a segment index; an encrypted container
	// always writes the index (at least the canary) so a wrong key is caught at
	// open even on an empty database. oldSegs are released after the durable flip.
	dirOffset, dirBlocks, dirChecksum, dirHash, oldSegs, err := c.writeDirectory()
	if err != nil {
		return err
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
		segEntries:    uint32(c.segEntries),
		generation:    newGen,
		codec:         c.codecTag(),
		enc:           c.enc,
		dirChecksum:   dirChecksum,
		keyslotOffset: c.keyslotOffset,
		authenticated: c.authenticated,
		dirHash:       dirHash,
	}
	if c.authenticated {
		if c.macKey != nil {
			// Symmetric mode: HMAC the committed state with the data-key-derived key.
			copy(sb.writerSig[:macTagLen], macTag(c.macKey, sb.signedState()))
		} else {
			// Writer mode: ed25519-sign the committed state (base fields + directory
			// hash). A container with no writer identity is read-only and never
			// reaches commit.
			if c.writeAs == nil {
				return errReadOnly
			}
			copy(sb.writerSig[:], keyring.SignState(c.writeAs, sb.signedState()))
		}
	}
	if _, err := c.back.WriteAt(sb.marshal(), c.nextSlot*int64(c.blockSize)); err != nil {
		return err
	}
	if err := c.back.Sync(); err != nil {
		return err
	}

	if c.committedDirBlocks > 0 {
		c.releaseLater(c.committedDirOffset, c.committedDirBlocks) // the old segment-index extent
	}
	for _, d := range oldSegs {
		if d.physOffset != 0 {
			c.releaseLater(d.physOffset, d.blocks) // the old directory-segment extents
		}
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
	c.dirtySegs = nil // every change since the last commit is now durable
	// Advance the external replay floor now that newGen is durable. The store is
	// surfaced as an error because the caller must know the anti-replay floor did
	// NOT advance — a lagging floor leaves a window in which a rollback to a
	// generation between the floor and newGen would still be accepted. The cost is
	// that this transaction is already durably committed (both fsyncs done) yet the
	// returned error makes SQLite treat the Sync as failed and roll back its
	// in-memory transaction; on the next open the file is at newGen >= floor and is
	// accepted, so there is no corruption, only a spurious failure on a rare anchor
	// I/O error. Anchors should therefore have reliable, durable storage.
	if c.anchor != nil {
		if err := c.anchor.StoreGeneration(newGen); err != nil {
			return fmt.Errorf("vault: replay anchor store: %w", err)
		}
	}
	return nil
}

// writeMetaExtent writes one directory-metadata extent — a directory segment, or
// the segment index — to a freshly allocated COW block run and returns where it
// landed plus its integrity fields. content is the marshaled bytes; withCanary
// prepends the wrong-key canary (the index only); tweak and domain key the cipher.
// Empty plaintext content writes nothing (off 0). hash is the on-disk (ciphertext)
// digest used in authenticated mode; the caller keeps its full 32 bytes for the
// index (superblock dirHash) or its truncation for a segment (segDesc.hash).
// Caller holds c.mu (write).
func (c *container) writeMetaExtent(content []byte, withCanary bool, tweak uint64, domain byte) (off uint64, blocks, storedLen, checksum uint32, hash [dirHashLen]byte, err error) {
	storedLen = uint32(len(content))
	if c.cipher != nil {
		prefix := 0
		if withCanary {
			prefix = dirCanaryLen
		}
		nb := blocksFor(uint64(prefix+len(content)), c.blockSize)
		if nb == 0 {
			nb = 1 // an encrypted extent always occupies at least one block (the canary)
		}
		buf := make([]byte, nb*c.blockSize)
		if withCanary {
			copy(buf, dirCanary[:])
		}
		copy(buf[prefix:], content)
		c.cipher.Encrypt(buf, tweak, domain)
		off = c.alloc.alloc(nb) * c.blockSize
		blocks = uint32(nb)
		checksum = crc32.Checksum(buf, crc32C)
		if c.authenticated {
			hash = sha256.Sum256(buf)
		}
		if _, werr := c.back.WriteAt(buf, int64(off)); werr != nil {
			return 0, 0, 0, 0, hash, werr
		}
		return off, blocks, storedLen, checksum, hash, nil
	}
	if len(content) == 0 {
		return 0, 0, 0, 0, hash, nil
	}
	checksum = crc32.Checksum(content, crc32C)
	nb := blocksFor(uint64(len(content)), c.blockSize)
	off = c.alloc.alloc(nb) * c.blockSize
	blocks = uint32(nb)
	if werr := c.writeBlocks(content, off, nb); werr != nil {
		return 0, 0, 0, 0, hash, werr
	}
	return off, blocks, storedLen, checksum, hash, nil
}

// writeDirectory persists the page directory as fixed-size segments plus a small
// segment index, and returns the index extent's location and integrity fields for
// the superblock, along with the previous segments for the caller to release after
// the durable commit. It carries an unchanged (clean) segment forward in place and
// rewrites only the dirty ones plus the index — the O(changed pages) path. The
// integrity chain is superblock(dirHash) → index → segDesc.hash → segment. Caller
// holds c.mu (write).
func (c *container) writeDirectory() (off uint64, blocks, checksum uint32, hash [dirHashLen]byte, released []segDesc, err error) {
	// Defensive: a (non-occurring) truncate-grow could leave pageCount past the
	// in-memory directory; pad so every segment slice below is in bounds.
	for uint64(len(c.dir)) < c.pageCount {
		c.dir = append(c.dir, dirEntry{})
	}
	// free schedules a segment's superseded on-disk extent for release after the
	// durable commit (the same COW discipline as data slots and the index).
	free := func(s uint64) {
		if s < uint64(len(c.segIndex)) && c.segIndex[s].physOffset != 0 {
			released = append(released, c.segIndex[s])
		}
	}
	nSegs := segmentCount(c.pageCount, c.segEntries)
	if nSegs == 0 && c.cipher == nil {
		for s := range uint64(len(c.segIndex)) { // plaintext empty: drop all segments + index
			free(s)
		}
		c.segIndex, c.dirtySegs = nil, nil
		return 0, 0, 0, hash, released, nil
	}
	newSegs := make([]segDesc, nSegs)
	for s := range nSegs {
		// Carry an unchanged existing segment forward in place — the O(changed
		// pages) win: its bytes are untouched, so it is neither re-encrypted nor
		// rewritten, and its extent is not released.
		if _, dirty := c.dirtySegs[s]; !dirty && s < uint64(len(c.segIndex)) {
			newSegs[s] = c.segIndex[s]
			continue
		}
		lo, hi := segmentBounds(s, c.segEntries, c.pageCount)
		if allSparse(c.dir[lo:hi]) {
			free(s) // an all-sparse segment is recorded as physOffset 0 (not written)
			continue
		}
		entries := marshalDirectory(c.dir[lo:hi], c.authenticated)
		soff, sblocks, slen, scrc, shash, werr := c.writeMetaExtent(entries, false, s, domainDirectory)
		if werr != nil {
			return 0, 0, 0, hash, nil, werr
		}
		d := segDesc{physOffset: soff, storedLen: slen, blocks: sblocks, checksum: scrc}
		copy(d.hash[:], shash[:slotHashLen])
		newSegs[s] = d
		free(s)
	}
	for s := nSegs; s < uint64(len(c.segIndex)); s++ { // segments dropped by a shrink
		if c.segIndex[s].physOffset != 0 {
			released = append(released, c.segIndex[s])
		}
	}
	// The index carries the wrong-key canary and is the root the superblock
	// hashes/signs; it is small (O(#segments)) and rewritten every commit.
	off, blocks, _, checksum, hash, err = c.writeMetaExtent(marshalSegmentIndex(newSegs), true, 0, domainSegIndex)
	if err != nil {
		return 0, 0, 0, hash, nil, err
	}
	// c.segIndex advances to the new extents here, before commit's durable superblock
	// flip. The COW invariant still holds — no block is freed in place; the superseded
	// extents are released only after the flip (see commit). The one consequence: if
	// commit then fails (a Sync/WriteAt error) and is retried on the SAME live
	// container, the extents this attempt would have released are no longer reachable
	// and leak for the session. That is benign — never a double-free or corruption —
	// and rebuildAllocator reclaims them on the next reopen (it counts only what the
	// committed superblock references).
	c.segIndex = newSegs
	return off, blocks, checksum, hash, released, nil
}

// allSparse reports whether every entry of a directory segment is sparse (never
// written), so the segment need not be stored on disk at all.
func allSparse(entries []dirEntry) bool {
	for _, e := range entries {
		if e.physOffset != 0 {
			return false
		}
	}
	return true
}

// markSegmentDirty records that the directory segment holding pageNo changed, so
// the next commit rewrites it instead of carrying it forward.
func (c *container) markSegmentDirty(pageNo uint64) {
	if c.segEntries == 0 {
		return
	}
	if c.dirtySegs == nil {
		c.dirtySegs = make(map[uint64]struct{})
	}
	c.dirtySegs[pageNo/c.segEntries] = struct{}{}
}

// --- vfs.File: in-process advisory locking via the shared vfs.AdvisoryLock ---

// Lock/Unlock/CheckReservedLock forward to the container's shared
// [vfs.AdvisoryLock], which arbitrates this connection against the others on the
// same container (many SHARED holders, one RESERVED..EXCLUSIVE writer).
func (f *mainFile) Lock(level vfs.LockLevel) error {
	return f.c.alock.Lock(f, &f.lock, level)
}

func (f *mainFile) Unlock(level vfs.LockLevel) error {
	return f.c.alock.Unlock(f, &f.lock, level)
}

func (f *mainFile) CheckReservedLock() (bool, error) {
	return f.c.alock.CheckReservedLock()
}

// ShmGroup implements vfs.ShmFile to unlock WAL: it returns the container's
// canonical path, so the dispatcher hands every connection on the same database
// one shared WAL index. The shared-memory regions and WAL lock table are the
// dispatcher's; this is all a File must declare. (An unshared container —
// tests / anonymous — returns "", a private group it never actually uses, since
// those handles are driven directly and never enter WAL.)
func (f *mainFile) ShmGroup() string { return f.c.name }

var _ vfs.ShmFile = (*mainFile)(nil)
