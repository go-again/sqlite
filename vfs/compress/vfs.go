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
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	sqlite "gosqlite.org"
	"gosqlite.org/crypto/keyring"
	"gosqlite.org/internal/cabi"
	"gosqlite.org/vfs"
	"gosqlite.org/vfs/crypto"
)

// VFS is a registered live compressing VFS. Its Close unregisters it; wire
// it into [sqlite.Config.VFSCloser] (as [Open] does) so a single
// db.Close() both drains the pool and releases the VFS.
type VFS struct {
	name      string
	blockSize uint64
	pageSize  uint64
	codec     Compression
	keyCfg    keyConfig         // encryption inputs from Options (raw key / recipients / identities)
	cipher    crypto.PageCipher // resolved at the main-DB open; nil = unencrypted. For the aux pass-through files.
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
		f, err := openMain(name, flags, v.blockSize, v.pageSize, v.codec, v.keyCfg)
		if err != nil {
			return nil, 0, err
		}
		// Cache the resolved data-key cipher so the journal/WAL pass-through files
		// (opened after the main DB) encrypt with it. SQLite always opens the main
		// DB first, so this is set in time.
		v.cipher = f.c.cipher
		return f, flags, nil
	}
	f, err := openPass(name, flags, v.cipher, int64(v.pageSize))
	if err != nil {
		return nil, 0, err
	}
	return f, flags, nil
}

// passDomain maps an auxiliary file's open flags to its cipher domain byte. The
// aux files share the container's cipher and data key, so each file kind needs a
// distinct domain — otherwise two concurrent temp files (e.g. a temp DB and a
// sub-journal) would encrypt the same offset to identical ciphertext, the
// cross-unit replay the (tweak, domain) split exists to prevent. The values are
// compress's own (disjoint from the persistent domainPageData/domainDirectory),
// not crypto.FileKind*, which would collide with those.
func passDomain(flags vfs.OpenFlags) byte {
	switch {
	case flags.Has(vfs.OpenMainJournal):
		return domainJournal
	case flags.Has(vfs.OpenWAL):
		return domainWAL
	case flags.Has(vfs.OpenTempDB):
		return domainTempDB
	case flags.Has(vfs.OpenTempJournal):
		return domainTempJournal
	case flags.Has(vfs.OpenSubJournal):
		return domainSubJournal
	default:
		return domainTempAux
	}
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
func openMain(path string, flags vfs.OpenFlags, blockSize, pageSize uint64, codec Compression, kc keyConfig) (*mainFile, error) {
	containers.mu.Lock()
	defer containers.mu.Unlock()

	if ct := containers.m[path]; ct != nil {
		// A second opener shares the live container, so it must hold the same key
		// — otherwise a wrong/absent key would silently inherit the first opener's
		// cipher (or a key would attach to a plaintext database).
		if err := ct.matchesKeyConfig(kc); err != nil {
			return nil, err
		}
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
	ct, err := newContainerOver(fileBacking{file}, readOnly, blockSize, pageSize, codec, kc)
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
	ct, err := newContainerOver(back, readOnly, blockSize, pageSize, codec, keyConfig{})
	if err != nil {
		return nil, err
	}
	ct.refs = 1
	return &mainFile{c: ct}, nil
}

// newContainerOver loads or initialises a container over an already-open
// backing. It closes back on any error.
func newContainerOver(back backing, readOnly bool, cfgBlockSize, cfgPageSize uint64, codec Compression, kc keyConfig) (*container, error) {
	size, err := back.Size()
	if err != nil {
		_ = back.Close()
		return nil, err
	}

	c := &container{back: back, blockSize: cfgBlockSize, pageSize: cfgPageSize, codec: codec, readOnly: readOnly}

	if size == 0 {
		c.alloc = newAllocator(nil, superblockBlocks)
		if readOnly {
			if kc.encrypting() {
				// A key was supplied for an empty, read-only file: there is nothing to
				// decrypt and we cannot initialise it. Fail loudly rather than hand back
				// a silent plaintext container that ignored the key.
				_ = back.Close()
				return nil, errors.New("compress: cannot open an empty database read-only with a key or identity")
			}
			return c, nil // empty read-only database: behaves as empty, never written
		}
		if err := c.initCipherForCreate(kc); err != nil { // resolve/generate the data key, write any keyslot
			_ = back.Close()
			return nil, fmt.Errorf("compress: init encryption: %w", err)
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

	// Resolve the cipher (a raw key, or unwrap a keyslot) now that the block size
	// is known; reserve the keyslot block in the allocator below.
	keyslotBlocks, err := c.resolveCipherForOpen(kc, sb, size)
	if err != nil {
		_ = back.Close()
		return nil, err
	}
	// A keyslot that authorizes writers REQUIRES authenticated mode. The on-disk
	// authenticated flag is otherwise unauthenticated, so a data-key holder (e.g. a
	// read-only recipient) could clear it — stripping the per-slot hashes and the
	// commit signature — while keeping the genuine master-signed keyslot, and a
	// reader pinning the right masters would accept the forged plaintext-integrity
	// state. c.writers comes from that master-verified keyslot, so binding it to the
	// flag closes the downgrade (a non-master cannot change the keyslot's writers).
	if len(c.writers) > 0 && !c.authenticated {
		_ = back.Close()
		return nil, ErrUnauthorized
	}
	if c.authenticated {
		// The committed state must be signed by an authorized writer (those the
		// master-signed keyslot pins); the directory bytes are checked against the
		// signed hash below. Without a writer identity this handle is read-only.
		if !keyring.VerifyState(c.writers, sb.signedState(), sb.writerSig[:]) {
			_ = back.Close()
			return nil, ErrUnauthorized
		}
		if c.writeAs == nil {
			c.readOnlyRecipient = true
		}
	}

	if sb.dirBlocks > 0 {
		dirBuf := make([]byte, uint64(sb.dirBlocks)*c.blockSize)
		if _, err := back.ReadAt(dirBuf, int64(sb.dirOffset)); err != nil {
			_ = back.Close()
			return nil, fmt.Errorf("read directory: %w", err)
		}
		var content []byte
		if c.cipher != nil {
			// In authenticated mode the on-disk directory must match the writer-signed
			// hash (checked over the ciphertext, before decryption mutates dirBuf).
			if c.authenticated && sha256.Sum256(dirBuf) != sb.dirHash {
				_ = back.Close()
				return nil, ErrUnauthorized
			}
			// Checksum the on-disk ciphertext, decrypt, then verify the canary
			// (wrong key) before reading the entries past it.
			if !c.readVerifyDecrypt(dirBuf, sb.dirChecksum, dirTweak, domainDirectory) {
				_ = back.Close()
				return nil, errors.New("directory checksum mismatch (corruption)")
			}
			if !bytes.Equal(dirBuf[:dirCanaryLen], dirCanary[:]) {
				_ = back.Close()
				return nil, ErrWrongKey
			}
			need := dirCanaryLen + int(sb.pageCount)*dirEntryBytes(sb.authenticated)
			if need > len(dirBuf) {
				_ = back.Close()
				return nil, errors.New("compress: directory too small for its page count (corruption)")
			}
			content = dirBuf[dirCanaryLen:need]
		} else {
			content = dirBuf[:int(sb.pageCount)*dirEntryBytes(sb.authenticated)]
			if crc32.Checksum(content, crc32C) != sb.dirChecksum {
				_ = back.Close()
				return nil, errors.New("directory checksum mismatch (corruption)")
			}
		}
		dir, err := parseDirectory(content, int(sb.pageCount), sb.authenticated)
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
	c.alloc = rebuildAllocator(c.dir, sb, size, keyslotBlocks)
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
// compression. It embeds NoLock (single-connection). When the database is
// encrypted it also encrypts these auxiliaries at rest, page-aligned by absolute
// offset with read-modify-write for sub-page writes — the same scheme vfs/crypto
// uses — so a transaction's page images never hit disk in the clear.
type passFile struct {
	vfs.NoLock
	f        *os.File
	temp     bool
	cipher   crypto.PageCipher // nil = plain passthrough
	pageSize int64
	domain   byte
}

func openPass(name string, flags vfs.OpenFlags, cipher crypto.PageCipher, pageSize int64) (*passFile, error) {
	if name == "" { // anonymous temp file: never shared, never reopened
		f, err := os.CreateTemp("", "gosqlitez-")
		if err != nil {
			return nil, err
		}
		return &passFile{f: f, temp: true, cipher: cipher, pageSize: pageSize, domain: passDomain(flags)}, nil
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
	return &passFile{f: f, cipher: cipher, pageSize: pageSize, domain: passDomain(flags)}, nil
}

// cryptPages encrypts or decrypts each whole page in span (a page-aligned run
// starting at baseOffset), tweaked by the page number and the file's domain.
func (p *passFile) cryptPages(span []byte, baseOffset int64, encrypt bool) {
	ps := p.pageSize
	for i := int64(0); i+ps <= int64(len(span)); i += ps {
		pageNum := uint64((baseOffset + i) / ps)
		if encrypt {
			p.cipher.Encrypt(span[i:i+ps], pageNum, p.domain)
		} else {
			p.cipher.Decrypt(span[i:i+ps], pageNum, p.domain)
		}
	}
}

func (p *passFile) ReadAt(b []byte, off int64) (int, error) {
	if p.cipher == nil {
		return p.f.ReadAt(b, off)
	}
	ps := p.pageSize
	pageStart := (off / ps) * ps
	pageEnd := (off + int64(len(b)) + ps - 1) / ps * ps
	span := make([]byte, pageEnd-pageStart)
	rn, rerr := p.f.ReadAt(span, pageStart)
	if rerr != nil && rerr != io.EOF {
		return 0, rerr
	}
	p.cryptPages(span[:(int64(rn)/ps)*ps], pageStart, false) // decrypt the full pages we read
	lo := off - pageStart
	avail := int64(rn) - lo
	if avail <= 0 {
		return 0, io.EOF
	}
	n := min(int64(len(b)), avail)
	copy(b[:n], span[lo:lo+n])
	if n < int64(len(b)) {
		return int(n), io.EOF // short read; the dispatcher zero-fills the tail
	}
	return int(n), nil
}

func (p *passFile) WriteAt(b []byte, off int64) (int, error) {
	if p.cipher == nil {
		return p.f.WriteAt(b, off)
	}
	ps := p.pageSize
	pageStart := (off / ps) * ps
	pageEnd := (off + int64(len(b)) + ps - 1) / ps * ps
	span := make([]byte, pageEnd-pageStart)
	if off == pageStart && int64(len(b)) == int64(len(span)) {
		copy(span, b) // full page-aligned span: overwrite entirely
	} else {
		// Read-modify-write: fetch the enclosing pages, decrypt, splice in b.
		rn, rerr := p.f.ReadAt(span, pageStart)
		if rerr != nil && rerr != io.EOF {
			return 0, rerr
		}
		p.cryptPages(span[:(int64(rn)/ps)*ps], pageStart, false)
		copy(span[off-pageStart:], b)
	}
	p.cryptPages(span, pageStart, true)
	if _, err := p.f.WriteAt(span, pageStart); err != nil {
		return 0, err
	}
	return len(b), nil
}
func (p *passFile) Truncate(n int64) error                 { return p.f.Truncate(n) }
func (p *passFile) Sync(vfs.SyncFlags) error               { return p.f.Sync() }
func (p *passFile) SectorSize() int                        { return defaultBlockSize }
func (p *passFile) DeviceCharacteristics() vfs.DeviceFlags { return 0 }

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
	kc, err := keyConfigFromOptions(opts)
	if err != nil {
		return nil, err
	}
	v := &VFS{
		name:      cabi.UniqueName("compressz"),
		blockSize: blockSize,
		pageSize:  pageSize,
		codec:     opts.Level,
		keyCfg:    kc,
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
