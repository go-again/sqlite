package compress

// live.go is the live compressing VFS: a pure-Go, file-backed implementation of
// the public vfs.VFS interface whose main-database file is a compressed
// container (mainfile.go) and whose journal/temp files pass straight through to
// the OS. Unlike the snapshot Open (compress.go) — which inflates to a plaintext
// working copy and recompresses at Close — this queries the database while it
// stays compressed on disk, durable per transaction.
//
// Phase 1 is single-connection, rollback-journal: OpenLive forces
// MaxOpenConns(1) and a rollback journal mode, and the files embed vfs.NoLock.
// WAL (which needs the shared-memory capability) and multi-connection locking
// are later increments.

import (
	"errors"
	"fmt"
	"hash/crc32"
	"maps"
	"os"
	"path/filepath"
	"strconv"

	sqlite "gosqlite.org"
	"gosqlite.org/internal/cabi"
	"gosqlite.org/vfs"
)

// LiveVFS is a registered live compressing VFS. Its Close unregisters it; wire
// it into [sqlite.Config.VFSCloser] (as [OpenLive] does) so a single
// db.Close() both drains the pool and releases the VFS.
type LiveVFS struct {
	name      string
	blockSize uint64
	pageSize  uint64
	codec     Compression
}

// Name is the registered VFS name, for use as sqlite.Config.VFS.
func (v *LiveVFS) Name() string { return v.name }

// Close unregisters the VFS. It is idempotent: a second call is a no-op.
func (v *LiveVFS) Close() error {
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
func (v *LiveVFS) Open(name string, flags vfs.OpenFlags) (vfs.File, vfs.OpenFlags, error) {
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
func (v *LiveVFS) Delete(name string, _ bool) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Access reports whether name exists / is accessible.
func (v *LiveVFS) Access(name string, _ vfs.AccessOp) (bool, error) {
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
func (v *LiveVFS) FullPathname(name string) (string, error) { return filepath.Abs(name) }

// openMain opens or creates the compressed main-database container at path.
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
	f, err := openMainOver(fileBacking{file}, readOnly, blockSize, pageSize, codec)
	if err != nil {
		return nil, fmt.Errorf("compress: open %q: %w", path, err)
	}
	return f, nil
}

// openMainOver builds a mainFile over an already-open backing — the seam tests
// use to drive the storage engine over an in-memory, fault-injecting store. It
// closes back on any error.
func openMainOver(back backing, readOnly bool, cfgBlockSize, cfgPageSize uint64, codec Compression) (*mainFile, error) {
	size, err := back.Size()
	if err != nil {
		_ = back.Close()
		return nil, err
	}

	f := &mainFile{back: back, blockSize: cfgBlockSize, pageSize: cfgPageSize, codec: codec, readOnly: readOnly}

	if size == 0 {
		f.alloc = newAllocator(nil, superblockBlocks)
		if readOnly {
			return f, nil // empty read-only database: behaves as empty, never written
		}
		if err := f.commit(); err != nil { // persist an empty committed container
			_ = back.Close()
			return nil, fmt.Errorf("initialise container: %w", err)
		}
		return f, nil
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

	f.blockSize = uint64(sb.blockSize)
	f.pageSize = uint64(sb.pageSize)
	f.pageCount = sb.pageCount
	f.committedGen = sb.generation
	f.committedDirOffset = sb.dirOffset
	f.committedDirBlocks = sb.dirBlocks
	f.nextSlot = int64(1 - slot)

	if sb.dirBlocks > 0 {
		dirBuf := make([]byte, uint64(sb.dirBlocks)*f.blockSize)
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
		f.dir = dir
	}
	f.alloc = rebuildAllocator(f.dir, sb, size)
	return f, nil
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
// for ensuring the database's page_size equals the resolved page size, for
// keeping the pool at one connection, and for calling Close to unregister.
// Most callers want [OpenLive], which wires all of that up.
func NewVFS(opts Options) (*LiveVFS, error) {
	blockSize, pageSize, err := opts.resolveLive()
	if err != nil {
		return nil, err
	}
	v := &LiveVFS{
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

// OpenLive opens cfg.Path as a database that stays compressed on disk, queried
// in place and durable per transaction (the ZIPVFS use case), pure Go.
//
//	db, err := compress.OpenLive(sqlite.Config{Path: "app.db.az"}, compress.Options{})
//	if err != nil { ... }
//	defer db.Close()
//
// It registers a live compressing VFS, routes cfg through it, and — via
// cfg.VFSCloser — unregisters it when the returned handle closes. Phase 1 is
// single-connection and rollback-journal, so OpenLive forces MaxOpenConns(1),
// sets the page size to match the container, disables mmap, and selects a
// rollback journal (overriding any WAL request). cfg.VFS must be empty and the
// path must be on disk.
//
// OpenLive is distinct from the snapshot [Open]: Open trades per-transaction
// durability and an at-rest-only footprint for simplicity (a plaintext working
// copy, recompressed at Close); OpenLive keeps the on-disk file compressed
// throughout and never materialises the whole database in the clear.
func OpenLive(cfg sqlite.Config, opts Options) (*sqlite.DB, error) {
	if cfg.VFS != "" {
		return nil, errors.New("compress: Config.VFS must be empty (OpenLive sets it to the live compressing VFS)")
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
	cfg.MaxOpenConns = 1
	// Rollback journal only (WAL needs the shm capability, a later increment).
	cfg.Pragmas.JournalMode = sqlite.JournalDelete
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
