package compress

// vfs.go is the live compressing VFS: a pure-Go, file-backed implementation of
// the public vfs.VFS interface whose main-database file is a compressed
// container (container.go) and whose journal/temp files pass straight through to
// the OS. Unlike the snapshot OpenSnapshot (snapshot.go) — which inflates to a
// plaintext working copy and recompresses at Close — this queries the database
// while it stays compressed on disk, durable per transaction.
//
// Multiple connections that open the same path share one container and
// coordinate through the in-process advisory locks (container.go). The default
// journal is rollback; WAL is available via the ShmFile capability (mainFile
// implements ShmGroup).

import (
	"errors"
	"fmt"
	"hash/crc32"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	sqlite "gosqlite.org"
	"gosqlite.org/internal/cabi"
	"gosqlite.org/vfs"
)

// VFS is a registered live compressing VFS. Its Close unregisters it; wire
// it into [sqlite.Config.VFSCloser] (as [Open] does) so a single
// db.Close() both drains the pool and releases the VFS.
type VFS struct {
	name      string
	blockSize uint64
	pageSize  uint64
	codec     Compression
}

// Name is the registered VFS name, for use as sqlite.Config.VFS.
func (v *VFS) Name() string { return v.name }

// Close unregisters the VFS. It is idempotent: a second call is a no-op.
func (v *VFS) Close() error {
	if v.name == "" {
		return nil
	}
	name := v.name
	v.name = ""
	return vfs.Unregister(name)
}

// Open routes the main database to a compressing File and everything else
// (rollback journal, temp DB/journal, super-/sub-journal) to a pass-through
// File. Journals are sequential and transient, so compressing them buys
// nothing and would only complicate recovery.
func (v *VFS) Open(name string, flags vfs.OpenFlags) (vfs.File, vfs.OpenFlags, error) {
	if flags.Has(vfs.OpenMainDB) {
		f, err := openMain(name, flags, v.blockSize, v.pageSize, v.codec)
		if err != nil {
			return nil, 0, err
		}
		return f, flags, nil
	}
	f, err := openPass(name, flags)
	if err != nil {
		return nil, 0, err
	}
	return f, flags, nil
}

// Delete removes a file (a journal, typically). A missing file is not an error.
func (v *VFS) Delete(name string, _ bool) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Access reports whether name exists / is accessible.
func (v *VFS) Access(name string, _ vfs.AccessOp) (bool, error) {
	switch _, err := os.Stat(name); {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// FullPathname canonicalises to an absolute path so a database and its journal
// siblings share a cache key.
func (v *VFS) FullPathname(name string) (string, error) { return filepath.Abs(name) }

// containers is the process-global registry of open compressed databases, keyed
// by canonical path. Every connection that opens the same path shares one
// container, so they observe the same committed state with no disk re-read.
var containers = struct {
	mu sync.Mutex
	m  map[string]*container
}{m: map[string]*container{}}

// openMain opens or creates the compressed main-database container at path and
// returns a connection handle that shares the (possibly already-open) container
// with other connections on the same path.
//
//   - empty file (size 0): a fresh container is initialised and an empty
//     committed superblock is persisted immediately, so the file always carries
//     a valid superblock — a crash before the first real commit reopens as an
//     empty database, and a foreign file is never mistaken for ours.
//   - existing container: the authoritative superblock is selected, the
//     directory loaded, and the allocator rebuilt by scanning it.
//   - existing non-container (e.g. a raw .db someone pointed us at): rejected,
//     so the file is never clobbered.
func openMain(path string, flags vfs.OpenFlags, blockSize, pageSize uint64, codec Compression) (*mainFile, error) {
	containers.mu.Lock()
	defer containers.mu.Unlock()

	if ct := containers.m[path]; ct != nil {
		ct.refs++
		return &mainFile{c: ct}, nil
	}

	readOnly := flags.Has(vfs.OpenReadOnly)
	oflag := os.O_RDWR
	if readOnly {
		oflag = os.O_RDONLY
	}
	if flags.Has(vfs.OpenCreate) {
		oflag |= os.O_CREATE
	}
	file, err := os.OpenFile(path, oflag, 0o600)
	if err != nil {
		return nil, err // dispatcher maps a not-exist error to SQLITE_CANTOPEN
	}
	ct, err := newContainerOver(fileBacking{file}, readOnly, blockSize, pageSize, codec)
	if err != nil {
		return nil, fmt.Errorf("compress: open %q: %w", path, err)
	}
	ct.name = path
	ct.refs = 1
	containers.m[path] = ct
	return &mainFile{c: ct}, nil
}

// release drops one handle's reference. When the last handle on a shared
// container closes, the backing is closed and the registry entry removed; an
// unshared container (tests / anonymous) just closes its backing.
func (c *container) release() error {
	if c.name == "" {
		c.refs--
		if c.refs > 0 {
			return nil
		}
		return c.back.Close()
	}
	containers.mu.Lock()
	defer containers.mu.Unlock()
	c.refs--
	if c.refs > 0 {
		return nil
	}
	delete(containers.m, c.name)
	return c.back.Close()
}

// openMainOver builds a single-handle, unshared mainFile over an already-open
// backing — the seam tests use to drive the storage engine over an in-memory,
// fault-injecting store.
func openMainOver(back backing, readOnly bool, blockSize, pageSize uint64, codec Compression) (*mainFile, error) {
	ct, err := newContainerOver(back, readOnly, blockSize, pageSize, codec)
	if err != nil {
		return nil, err
	}
	ct.refs = 1
	return &mainFile{c: ct}, nil
}

// newContainerOver loads or initialises a container over an already-open
// backing. It closes back on any error.
func newContainerOver(back backing, readOnly bool, cfgBlockSize, cfgPageSize uint64, codec Compression) (*container, error) {
	size, err := back.Size()
	if err != nil {
		_ = back.Close()
		return nil, err
	}

	c := &container{back: back, blockSize: cfgBlockSize, pageSize: cfgPageSize, codec: codec, readOnly: readOnly}

	if size == 0 {
		c.alloc = newAllocator(nil, superblockBlocks)
		if readOnly {
			return c, nil // empty read-only database: behaves as empty, never written
		}
		if err := c.commit(); err != nil { // persist an empty committed container
			_ = back.Close()
			return nil, fmt.Errorf("initialise container: %w", err)
		}
		return c, nil
	}

	// Superblock A is always at offset 0; read it first to learn the on-disk
	// block size, then read superblock B at that block size. The two copies are
	// fixed-location once the block size is known, so ping-pong recovery does
	// not depend on A being intact (for the common default-block-size case, B is
	// found even when A is corrupt).
	sbA := readSuperblockAt(back, 0)
	bs := cfgBlockSize
	if a, e := parseSuperblock(sbA); e == nil {
		bs = uint64(a.blockSize)
	}
	sbB := readSuperblockAt(back, int64(bs))

	sb, slot, err := pickSuperblockSlot(sbA, sbB)
	if err != nil {
		_ = back.Close()
		return nil, fmt.Errorf("not a compressed container: %w", err)
	}
	// Bound the superblock fields before using them: the container may be
	// untrusted, and the CRC alone does not stop an adversary choosing hostile
	// values (a crafted file would otherwise panic or exhaust memory here).
	if err := sb.validate(size); err != nil {
		_ = back.Close()
		return nil, err
	}

	c.blockSize = uint64(sb.blockSize)
	c.pageSize = uint64(sb.pageSize)
	c.pageCount = sb.pageCount
	c.committedGen = sb.generation
	c.committedDirOffset = sb.dirOffset
	c.committedDirBlocks = sb.dirBlocks
	c.nextSlot = int64(1 - slot)

	if sb.dirBlocks > 0 {
		dirBuf := make([]byte, uint64(sb.dirBlocks)*c.blockSize)
		if _, err := back.ReadAt(dirBuf, int64(sb.dirOffset)); err != nil {
			_ = back.Close()
			return nil, fmt.Errorf("read directory: %w", err)
		}
		content := dirBuf[:int(sb.pageCount)*dirEntrySize]
		if crc32.Checksum(content, crc32C) != sb.dirChecksum {
			_ = back.Close()
			return nil, errors.New("directory checksum mismatch (corruption)")
		}
		dir, err := parseDirectory(content, int(sb.pageCount))
		if err != nil {
			_ = back.Close()
			return nil, fmt.Errorf("parse directory: %w", err)
		}
		if err := validateDirectory(dir, sb, size); err != nil {
			_ = back.Close()
			return nil, err
		}
		c.dir = dir
	}
	c.alloc = rebuildAllocator(c.dir, sb, size)
	return c, nil
}

// readSuperblockAt reads the superblock at byte offset off, tolerating a
// short/absent read (the alternate copy may not have been written yet) by
// leaving the unread bytes zero — which parses as an invalid superblock.
func readSuperblockAt(back backing, off int64) []byte {
	buf := make([]byte, superblockSize)
	_, _ = back.ReadAt(buf, off)
	return buf
}

// fileBacking adapts *os.File to the backing interface.
type fileBacking struct{ *os.File }

func (f fileBacking) Size() (int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// passFile is a thin *os.File wrapper for journals and temp files: no
// translation, no compression. It embeds NoLock (single-connection).
type passFile struct {
	vfs.NoLock
	f    *os.File
	temp bool
}

func openPass(name string, flags vfs.OpenFlags) (*passFile, error) {
	if name == "" { // anonymous temp file: never shared, never reopened
		f, err := os.CreateTemp("", "gosqlitez-")
		if err != nil {
			return nil, err
		}
		return &passFile{f: f, temp: true}, nil
	}
	oflag := os.O_RDWR
	if flags.Has(vfs.OpenReadOnly) {
		oflag = os.O_RDONLY
	}
	if flags.Has(vfs.OpenCreate) {
		oflag |= os.O_CREATE
	}
	if flags.Has(vfs.OpenExclusive) {
		oflag |= os.O_EXCL
	}
	f, err := os.OpenFile(name, oflag, 0o600)
	if err != nil {
		return nil, err
	}
	return &passFile{f: f}, nil
}

func (p *passFile) ReadAt(b []byte, off int64) (int, error)  { return p.f.ReadAt(b, off) }
func (p *passFile) WriteAt(b []byte, off int64) (int, error) { return p.f.WriteAt(b, off) }
func (p *passFile) Truncate(n int64) error                   { return p.f.Truncate(n) }
func (p *passFile) Sync(vfs.SyncFlags) error                 { return p.f.Sync() }
func (p *passFile) SectorSize() int                          { return defaultBlockSize }
func (p *passFile) DeviceCharacteristics() vfs.DeviceFlags   { return 0 }

func (p *passFile) Size() (int64, error) {
	fi, err := p.f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (p *passFile) Close() error {
	err := p.f.Close()
	if p.temp {
		_ = os.Remove(p.f.Name())
	}
	return err
}

// NewVFS registers a live compressing VFS configured by opts and returns it.
// The caller is responsible for using the returned name as sqlite.Config.VFS,
// for ensuring the database's page_size equals the resolved page size, and for
// calling Close to unregister. Most callers want [Open], which wires all of
// that up.
func NewVFS(opts Options) (*VFS, error) {
	blockSize, pageSize, err := opts.resolveLive()
	if err != nil {
		return nil, err
	}
	v := &VFS{
		name:      cabi.UniqueName("compressz"),
		blockSize: blockSize,
		pageSize:  pageSize,
		codec:     opts.Level,
	}
	if err := vfs.Register(v.name, v); err != nil {
		return nil, err
	}
	return v, nil
}

// Open opens cfg.Path as a database that stays compressed on disk, queried
// in place and durable per transaction (the ZIPVFS use case), pure Go.
//
//	db, err := compress.Open(sqlite.Config{Path: "app.db.az"}, compress.Options{})
//	if err != nil { ... }
//	defer db.Close()
//
// It registers a live compressing VFS, routes cfg through it, and — via
// cfg.VFSCloser — unregisters it when the returned handle closes. Multiple
// pooled connections are supported: they share one in-memory container and
// coordinate through the VFS's in-process advisory locks (many readers, one
// writer). Open sets the page size to match the container, disables mmap,
// defaults a busy timeout, and selects a rollback journal (WAL needs the
// shared-memory capability and is a later increment, so a WAL request is
// overridden). cfg.VFS must be empty and the path must be on disk.
//
// Open is distinct from the snapshot [OpenSnapshot]: Open trades per-transaction
// durability and an at-rest-only footprint for simplicity (a plaintext working
// copy, recompressed at Close); Open keeps the on-disk file compressed
// throughout and never materialises the whole database in the clear.
func Open(cfg sqlite.Config, opts Options) (*sqlite.DB, error) {
	if cfg.VFS != "" {
		return nil, errors.New("compress: Config.VFS must be empty (Open sets it to the live compressing VFS)")
	}
	if cfg.Path == sqlite.InMemory || cfg.Mode == sqlite.ModeMemory {
		return nil, errors.New("compress: a compressed database requires an on-disk path (refusing :memory: / mode=memory)")
	}
	if cfg.Path == "" {
		return nil, errors.New("compress: Config.Path is required")
	}

	v, err := NewVFS(opts)
	if err != nil {
		return nil, err
	}

	cfg.VFS = v.name
	cfg.VFSCloser = v
	// Multiple connections are allowed and coordinate through the VFS's
	// in-process advisory locks; default a busy timeout so writer contention
	// retries rather than failing immediately. Rollback journal is the default
	// (no uncompressed working set on disk); the caller may request WAL — the
	// main DB stays compressed and only the transient -wal frames are
	// uncompressed, folded into compressed slots on checkpoint.
	if cfg.Pragmas.JournalMode == "" {
		cfg.Pragmas.JournalMode = sqlite.JournalDelete
	}
	if cfg.Pragmas.BusyTimeout == 0 {
		cfg.Pragmas.BusyTimeout = 5 * time.Second
	}
	extra := map[string]string{}
	maps.Copy(extra, cfg.Pragmas.Extra)
	extra["page_size"] = strconv.FormatUint(v.pageSize, 10)
	extra["mmap_size"] = "0"
	cfg.Pragmas.Extra = extra

	db, err := sqlite.Open(cfg)
	if err != nil {
		// sqlite.Open closes cfg.VFSCloser on its error paths; Close again
		// defensively (idempotent) in case it didn't.
		_ = v.Close()
		return nil, err
	}
	return db, nil
}
