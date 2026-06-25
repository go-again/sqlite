package vault

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
	"crypto/hmac"
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
	name       string
	blockSize  uint64
	pageSize   uint64
	segEntries uint64
	codec      Compression
	keyCfg     keyConfig // encryption inputs from Options (raw key / recipients / identities)

	// cipher is the data-key cipher resolved at the main-DB open, cached so the
	// aux pass-through files (journal/WAL/temp) encrypt with it; nil = unencrypted.
	// One VFS backs one database, so every open resolves the SAME cipher — but
	// pooled connections can open concurrently (each cold connection triggers its
	// own Open), so the write and the aux-open read are guarded to make the
	// publication well-defined under the race detector.
	//
	// preserveNewFiles, when set (only by [CompactLogical]), makes a NEW main-DB file
	// created through this VFS — a VACUUM INTO target — seal to the FIRST-opened
	// database's key and membership rather than failing (identity-only opts cannot
	// create) or re-sealing under a fresh key. preserve is that captured key material,
	// taken from the source open and applied to the target create; both are guarded by
	// cipherMu.
	cipherMu         sync.Mutex
	cipher           crypto.PageCipher
	preserveNewFiles bool
	preserve         *preservedKey
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
		kc := v.keyCfg
		if v.preserveNewFiles {
			// Seal a VACUUM INTO target to the source's key/membership: nil on the first
			// (source) open, set afterward, so initCipherForCreate preserves it.
			v.cipherMu.Lock()
			kc.preserve = v.preserve
			v.cipherMu.Unlock()
		}
		f, err := openMain(name, flags, v.blockSize, v.pageSize, v.segEntries, v.codec, kc)
		if err != nil {
			return nil, 0, err
		}
		// Cache the resolved data-key cipher so the journal/WAL pass-through files
		// (opened after the main DB) encrypt with it. SQLite always opens the main
		// DB first, so this is set in time. When preserving (CompactLogical), capture
		// the first encrypted recipients open's key material for the VACUUM INTO target.
		v.cipherMu.Lock()
		v.cipher = f.c.cipher
		if v.preserveNewFiles && v.preserve == nil && f.c.cipher != nil && f.c.keyslotOffset != 0 {
			v.preserve = &preservedKey{
				dek: f.c.dek, keyslot: f.c.keyslotBlob, enc: f.c.enc,
				auth: f.c.authenticated, writers: f.c.writers, writeAs: f.c.writeAs,
			}
		}
		v.cipherMu.Unlock()
		return f, flags, nil
	}
	v.cipherMu.Lock()
	cipher := v.cipher
	v.cipherMu.Unlock()
	f, err := openPass(name, flags, cipher)
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
// vault's own (disjoint from the persistent domainPageData/domainDirectory),
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
	mu     sync.Mutex
	m      map[string]*container
	locked map[string]struct{} // canonical paths reserved by an offline op (Compact/Rewrap/Rekey)
}{m: map[string]*container{}, locked: map[string]struct{}{}}

// reservePath marks a canonical path as exclusively held by an offline operation
// so a concurrent Open cannot register a live container over it mid-rewrite. It
// returns false if the path is already open or already reserved. Pair with
// releasePath. This closes the check-then-open race in Compact/Rewrap/Rekey: the
// check and the take are atomic under containers.mu.
func reservePath(abs string) bool {
	containers.mu.Lock()
	defer containers.mu.Unlock()
	if containers.m[abs] != nil {
		return false
	}
	if _, ok := containers.locked[abs]; ok {
		return false
	}
	containers.locked[abs] = struct{}{}
	return true
}

func releasePath(abs string) {
	containers.mu.Lock()
	delete(containers.locked, abs)
	containers.mu.Unlock()
}

// requireOnDiskPath validates the precondition shared by Open, OpenSnapshot, and
// Compact: a non-empty on-disk path, and no caller-supplied VFS (vault sets its
// own).
func requireOnDiskPath(cfg sqlite.Config) error {
	if cfg.VFS != "" {
		return errors.New("vault: Config.VFS must be empty (vault sets its own VFS)")
	}
	if cfg.Path == sqlite.InMemory || cfg.Mode == sqlite.ModeMemory {
		return errors.New("vault: an on-disk path is required (refusing :memory: / mode=memory)")
	}
	if cfg.Path == "" {
		return errors.New("vault: Config.Path is required")
	}
	return nil
}

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
func openMain(path string, flags vfs.OpenFlags, blockSize, pageSize, segEntries uint64, codec Compression, kc keyConfig) (*mainFile, error) {
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
	if _, busy := containers.locked[path]; busy {
		// An offline operation (Compact/Rewrap/Rekey) is rewriting this file; opening
		// a live container over it now would race two writers onto the same bytes.
		return nil, fmt.Errorf("vault: %q is busy (offline maintenance in progress)", path)
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
	ct, err := newContainerOver(fileBacking{file}, readOnly, blockSize, pageSize, segEntries, codec, kc)
	if err != nil {
		return nil, fmt.Errorf("vault: open %q: %w", path, err)
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
		// An unshared container (tests / anonymous / offline ops) is single-handle —
		// never in the registry, so no second goroutine can reach it; the refs dance
		// here needs no containers.mu (the shared branch below holds it).
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
func openMainOver(back backing, readOnly bool, blockSize, pageSize, segEntries uint64, codec Compression) (*mainFile, error) {
	ct, err := newContainerOver(back, readOnly, blockSize, pageSize, segEntries, codec, keyConfig{})
	if err != nil {
		return nil, err
	}
	ct.refs = 1
	return &mainFile{c: ct}, nil
}

// newContainerOver loads or initialises a container over an already-open
// backing. It closes back on any error.
func newContainerOver(back backing, readOnly bool, cfgBlockSize, cfgPageSize, cfgSegEntries uint64, codec Compression, kc keyConfig) (*container, error) {
	size, err := back.Size()
	if err != nil {
		_ = back.Close()
		return nil, err
	}

	c := &container{back: back, blockSize: cfgBlockSize, pageSize: cfgPageSize, segEntries: cfgSegEntries, codec: codec, readOnly: readOnly, anchor: kc.anchor}

	// The external replay floor (if any). Loaded once: it gates an empty file
	// (truncation/rollback to before any commit) here, and the committed generation
	// of a real container after its signed root is verified below.
	var anchorFloor uint64
	if kc.anchor != nil {
		f, err := kc.anchor.LoadGeneration()
		if err != nil {
			_ = back.Close()
			return nil, fmt.Errorf("vault: replay anchor load: %w", err)
		}
		anchorFloor = f
	}

	if size == 0 {
		if anchorFloor > 0 {
			// The anchor records a committed generation, but the file is empty — it
			// was rolled back (or truncated) to before that state.
			_ = back.Close()
			return nil, ErrRolledBack
		}
		c.alloc = newAllocator(nil, superblockBlocks)
		if readOnly {
			if kc.encrypting() {
				// A key was supplied for an empty, read-only file: there is nothing to
				// decrypt and we cannot initialise it. Fail loudly rather than hand back
				// a silent plaintext container that ignored the key.
				_ = back.Close()
				return nil, errors.New("vault: cannot open an empty database read-only with a key or identity")
			}
			return c, nil // empty read-only database: behaves as empty, never written
		}
		if err := c.initCipherForCreate(kc); err != nil { // resolve/generate the data key, write any keyslot
			_ = back.Close()
			return nil, fmt.Errorf("vault: init encryption: %w", err)
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
	c.segEntries = uint64(sb.segEntries)
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
		if c.macKey != nil {
			// Symmetric mode: recompute the HMAC root and compare in constant time.
			if !hmac.Equal(macTag(c.macKey, sb.signedState()), sb.writerSig[:macTagLen]) {
				_ = back.Close()
				return nil, ErrTampered
			}
		} else {
			// Writer mode: the committed state must be signed by an authorized writer
			// (those the master-signed keyslot pins). Without a writer identity this
			// handle is read-only.
			if !keyring.VerifyState(c.writers, sb.signedState(), sb.writerSig[:]) {
				_ = back.Close()
				return nil, ErrUnauthorized
			}
			if c.writeAs == nil {
				c.readOnlyRecipient = true
			}
		}
	}

	// Anti-replay: the committed generation is now authenticated (verified above),
	// so an external floor can reject a complete-but-stale earlier image.
	if c.committedGen < anchorFloor {
		_ = back.Close()
		return nil, ErrRolledBack
	}

	if sb.dirBlocks > 0 {
		nSegs := segmentCount(sb.pageCount, uint64(sb.segEntries))
		idxLen := int(nSegs) * segDescSize

		// Read, verify, and decrypt the segment index (the canary catches a wrong
		// key; in authenticated mode the superblock-signed dirHash covers it).
		idxBuf := make([]byte, uint64(sb.dirBlocks)*c.blockSize)
		if _, err := back.ReadAt(idxBuf, int64(sb.dirOffset)); err != nil {
			_ = back.Close()
			return nil, fmt.Errorf("read segment index: %w", err)
		}
		var idxContent []byte
		if c.cipher != nil {
			if c.authenticated && sha256.Sum256(idxBuf) != sb.dirHash {
				_ = back.Close()
				if c.macKey != nil { // symmetric mode reports integrity failures as ErrTampered
					return nil, ErrTampered
				}
				return nil, ErrUnauthorized
			}
			if !c.readVerifyDecrypt(idxBuf, sb.dirChecksum, 0, domainSegIndex) {
				_ = back.Close()
				return nil, errors.New("segment index checksum mismatch (corruption)")
			}
			if !bytes.Equal(idxBuf[:dirCanaryLen], dirCanary[:]) {
				_ = back.Close()
				return nil, ErrWrongKey
			}
			if dirCanaryLen+idxLen > len(idxBuf) {
				_ = back.Close()
				return nil, errors.New("vault: segment index too small for its segment count (corruption)")
			}
			idxContent = idxBuf[dirCanaryLen : dirCanaryLen+idxLen]
		} else {
			if idxLen > len(idxBuf) {
				_ = back.Close()
				return nil, errors.New("vault: segment index too small for its segment count (corruption)")
			}
			idxContent = idxBuf[:idxLen]
			if crc32.Checksum(idxContent, crc32C) != sb.dirChecksum {
				_ = back.Close()
				return nil, errors.New("segment index checksum mismatch (corruption)")
			}
		}
		segIndex, err := parseSegmentIndex(idxContent, int(nSegs))
		if err != nil {
			_ = back.Close()
			return nil, fmt.Errorf("parse segment index: %w", err)
		}

		// Read each (non-sparse) segment, verify it against its index descriptor,
		// decrypt, and assemble the directory.
		dir := make([]dirEntry, sb.pageCount)
		for s := range nSegs {
			d := segIndex[s]
			if d.physOffset == 0 {
				continue // an all-sparse segment: its entries stay zero
			}
			span := uint64(d.blocks) * c.blockSize
			if d.physOffset%c.blockSize != 0 || d.blocks == 0 || d.physOffset+span < d.physOffset || d.physOffset+span > uint64(size) {
				_ = back.Close()
				return nil, fmt.Errorf("vault: directory segment %d extent out of bounds", s)
			}
			lo, hi := segmentBounds(s, uint64(sb.segEntries), sb.pageCount)
			n := int(hi - lo)
			need := n * dirEntryBytes(sb.authenticated)
			segBuf := make([]byte, span)
			if _, err := back.ReadAt(segBuf, int64(d.physOffset)); err != nil {
				_ = back.Close()
				return nil, fmt.Errorf("read directory segment %d: %w", s, err)
			}
			if need > len(segBuf) {
				_ = back.Close()
				return nil, fmt.Errorf("vault: directory segment %d too small (corruption)", s)
			}
			if c.cipher != nil {
				if c.authenticated {
					var h [slotHashLen]byte
					full := sha256.Sum256(segBuf)
					copy(h[:], full[:slotHashLen])
					if h != d.hash {
						_ = back.Close()
						if c.macKey != nil {
							return nil, ErrTampered
						}
						return nil, ErrUnauthorized
					}
				}
				if !c.readVerifyDecrypt(segBuf, d.checksum, s, domainDirectory) {
					_ = back.Close()
					return nil, fmt.Errorf("vault: directory segment %d checksum mismatch (corruption)", s)
				}
			} else if crc32.Checksum(segBuf[:need], crc32C) != d.checksum {
				_ = back.Close()
				return nil, fmt.Errorf("vault: directory segment %d checksum mismatch (corruption)", s)
			}
			segDir, err := parseDirectory(segBuf[:need], n, sb.authenticated)
			if err != nil {
				_ = back.Close()
				return nil, fmt.Errorf("parse directory segment %d: %w", s, err)
			}
			copy(dir[lo:hi], segDir)
		}
		if err := validateDirectory(dir, segIndex, sb, size); err != nil {
			_ = back.Close()
			return nil, err
		}
		c.dir = dir
		c.segIndex = segIndex
	}
	c.alloc = rebuildAllocator(c.dir, c.segIndex, sb, size, keyslotBlocks)
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

// Sync uses a write barrier (F_BARRIERFSYNC on darwin) rather than the full
// device-cache flush os.File.Sync issues (F_FULLFSYNC). The container's copy-on-write
// commit needs write ORDERING, not a platter flush, for crash consistency; the barrier
// is ~30x cheaper on macOS. See syncBacking (sync_darwin.go) for the durability rationale.
func (f fileBacking) Sync() error { return syncBacking(f.File) }

func (f fileBacking) Size() (int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (f fileBacking) Truncate(size int64) error { return f.File.Truncate(size) }

// auxCryptUnit is the cipher alignment unit for the transient aux files
// (journal / WAL / temp). It is deliberately SMALL and decoupled from the DB page
// size: SQLite writes the WAL as stride-(24+pageSize) frames whose 24-byte frame
// headers never land on a page boundary, so aligning the cipher to the page size
// turned every header write into a full-page read-modify-write (measured ~8× cipher
// and ~5× write amplification on a large sequential write). A 512-byte unit bounds
// each RMW to one sector and leaves the bulk of a frame's page data on whole-unit
// boundaries, so the aux path runs at close to the plaintext rate. 512 is also
// Adiantum's design sector size, so the wide-block guarantee is unweakened. The
// unit is a code constant, never stored: aux files are recreated each session (and
// crash-recovered by the same build), so there is no on-disk compatibility concern.
const auxCryptUnit int64 = 512

// passFile is a thin *os.File wrapper for journals and temp files: no
// compression. It embeds NoLock (single-connection). When the database is
// encrypted it also encrypts these auxiliaries at rest, aligned to auxCryptUnit by
// absolute offset with read-modify-write for sub-unit writes, so a transaction's
// page images never hit disk in the clear.
type passFile struct {
	vfs.NoLock
	f      *os.File
	temp   bool
	cipher crypto.PageCipher // nil = plain passthrough
	domain byte
}

func openPass(name string, flags vfs.OpenFlags, cipher crypto.PageCipher) (*passFile, error) {
	if name == "" { // anonymous temp file: never shared, never reopened
		f, err := os.CreateTemp("", "gosqlitez-")
		if err != nil {
			return nil, err
		}
		return &passFile{f: f, temp: true, cipher: cipher, domain: passDomain(flags)}, nil
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
	return &passFile{f: f, cipher: cipher, domain: passDomain(flags)}, nil
}

// cryptPages encrypts or decrypts each whole auxCryptUnit-sized unit in span (a
// unit-aligned run starting at baseOffset), tweaked by the unit number and the
// file's domain.
//
// On decrypt it skips a unit whose on-disk bytes are all zero: that is a sparse
// hole (a region the file never wrote, e.g. a gap an extending write left behind),
// which must read back as zeros — decrypting it would yield garbage. Real
// ciphertext is pseudo-random, so an all-zero unit is never genuine encrypted data
// (a coincidental all-zero ciphertext has ~2^-4096 probability). This restores the
// "unwritten regions read as zeros" property that page-granularity materialization
// gave before the unit shrank, with no extra I/O.
func (p *passFile) cryptPages(span []byte, baseOffset int64, encrypt bool) {
	u := auxCryptUnit
	for i := int64(0); i+u <= int64(len(span)); i += u {
		unit := span[i : i+u]
		if !encrypt && allZero(unit) {
			continue // sparse hole: leave it zero rather than decrypt to garbage
		}
		unitNum := uint64((baseOffset + i) / u)
		if encrypt {
			p.cipher.Encrypt(unit, unitNum, p.domain)
		} else {
			p.cipher.Decrypt(unit, unitNum, p.domain)
		}
	}
}

// allZero reports whether b is entirely zero bytes, early-exiting on the first
// non-zero (so a real ciphertext unit costs ~one byte to reject).
func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func (p *passFile) ReadAt(b []byte, off int64) (int, error) {
	if p.cipher == nil {
		return p.f.ReadAt(b, off)
	}
	u := auxCryptUnit
	unitStart := (off / u) * u
	unitEnd := (off + int64(len(b)) + u - 1) / u * u
	bp := getExtent(uint64(unitEnd - unitStart)) // pooled; only [0,rn) is read back below
	defer putExtent(bp)
	span := *bp
	rn, rerr := p.f.ReadAt(span, unitStart)
	if rerr != nil && rerr != io.EOF {
		return 0, rerr
	}
	p.cryptPages(span[:(int64(rn)/u)*u], unitStart, false) // decrypt the full units we read
	lo := off - unitStart
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
	u := auxCryptUnit
	unitStart := (off / u) * u
	unitEnd := (off + int64(len(b)) + u - 1) / u * u
	bp := getExtent(uint64(unitEnd - unitStart))
	defer putExtent(bp)
	span := *bp
	if off == unitStart && int64(len(b)) == int64(len(span)) {
		copy(span, b) // unit-aligned span: overwrite entirely (no read, no pre-clear)
	} else {
		// Read-modify-write: fetch the enclosing units, decrypt, splice in b. The
		// pooled buffer is not pre-zeroed, so clear it first to reproduce make()'s
		// zero padding for any hole/tail the read and b do not cover.
		clear(span)
		rn, rerr := p.f.ReadAt(span, unitStart)
		if rerr != nil && rerr != io.EOF {
			return 0, rerr
		}
		p.cryptPages(span[:(int64(rn)/u)*u], unitStart, false)
		copy(span[off-unitStart:], b)
	}
	p.cryptPages(span, unitStart, true)
	if _, err := p.f.WriteAt(span, unitStart); err != nil {
		return 0, err
	}
	return len(b), nil
}
func (p *passFile) Truncate(n int64) error                 { return p.f.Truncate(n) }
func (p *passFile) Sync(vfs.SyncFlags) error               { return syncBacking(p.f) } // barrier, not F_FULLFSYNC (see syncBacking)
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
	blockSize, pageSize, segEntries, err := opts.resolveLive()
	if err != nil {
		return nil, err
	}
	kc, err := keyConfigFromOptions(opts)
	if err != nil {
		return nil, err
	}
	v := &VFS{
		name:       cabi.UniqueName("vault"),
		blockSize:  blockSize,
		pageSize:   pageSize,
		segEntries: segEntries,
		codec:      opts.Level,
		keyCfg:     kc,
	}
	if err := vfs.Register(v.name, v); err != nil {
		return nil, err
	}
	return v, nil
}

// Open opens cfg.Path as a database that stays compressed on disk, queried
// in place and durable per transaction (the ZIPVFS use case), pure Go.
//
//	db, err := vault.Open(sqlite.Config{Path: "app.db.az"}, vault.Options{})
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
	if err := requireOnDiskPath(cfg); err != nil {
		return nil, err
	}
	return openThroughVault(cfg, opts, false)
}

// openThroughVault registers a vault VFS for opts, wires it into cfg with the
// standard pragmas (page size, no mmap, a default busy timeout and journal), and
// opens the database. preserveNewFiles makes a NEW main-DB file created through the
// VFS — a VACUUM INTO target — seal to the first-opened database's key and
// membership; only [CompactLogical] sets it. The caller has already validated cfg.
func openThroughVault(cfg sqlite.Config, opts Options, preserveNewFiles bool) (*sqlite.DB, error) {
	v, err := NewVFS(opts)
	if err != nil {
		return nil, err
	}
	v.preserveNewFiles = preserveNewFiles

	cfg.VFS = v.name
	cfg.VFSCloser = v
	// The container serves whole logical pages of v.pageSize, so SQLite MUST use that
	// page_size — otherwise every SQLite write is a sub-page read-modify-write of a
	// container slot, and the page-number addressing the cipher and the freelist reader
	// rely on no longer lines up. page_size only takes hold before the database's first
	// write, but the OTHER creation-time pragmas — auto_vacuum and journal_mode = WAL —
	// write the header, and the driver applies the _pragma flags SORTED, so either would
	// run before page_size and lock the page size at SQLite's default. So keep only
	// page_size (and the harmless per-connection pragmas) in the DSN, and apply
	// auto_vacuum then journal_mode AFTER open — page_size pending, then auto_vacuum
	// (writes the header at that size), then the journal — so the container's page size
	// is the one that sticks.
	journal := cfg.Pragmas.JournalMode
	if journal == "" {
		journal = sqlite.JournalDelete // no uncompressed working set on disk by default
	}
	autoVacuum := cfg.Pragmas.AutoVacuum
	cfg.Pragmas.JournalMode = ""
	cfg.Pragmas.AutoVacuum = ""

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
	// page_size is pending; set the header-writing pragmas in order — auto_vacuum
	// before the journal, both before the database gets any content.
	setup := make([]string, 0, 2)
	if autoVacuum != "" {
		setup = append(setup, "PRAGMA auto_vacuum = "+string(autoVacuum))
	}
	setup = append(setup, "PRAGMA journal_mode = "+string(journal))
	for _, p := range setup {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("vault: %s: %w", p, err)
		}
	}
	return db, nil
}
