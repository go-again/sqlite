package vault

import (
	"fmt"

	"gosqlite.org/crypto/keyring"
	"gosqlite.org/vfs/crypto"
)

// Compression selects the at-rest compression level. It is an independent enum
// parallel to (but deliberately not shared with) the one in
// [gosqlite.org/blobstore] — a separate module. CompressionNone (the default)
// stores pages raw — compression is off; any other value compresses at that
// level. Encryption
// is independent (see Key/Recipients), so a database may be compressed,
// encrypted, both, or neither.
type Compression int

const (
	// CompressionNone is the zero value: compression off (pages stored raw).
	CompressionNone Compression = iota
	// CompressionFastest is the fastest, lowest-ratio level (LZ4).
	CompressionFastest
	// CompressionFast trades a little speed for ratio (LZ4 HC).
	CompressionFast
	// CompressionDefault is a balanced level (zstd) — the default.
	CompressionDefault
	// CompressionBetter spends more CPU for a better ratio (zstd).
	CompressionBetter
	// CompressionBest is the slowest, highest-ratio level (zstd).
	CompressionBest
)

// Options configures a database opened with [Open] or [OpenSnapshot]. Compression
// and encryption are independent: leave both unset for a plain container, set
// Level to compress, set Key/Recipients to encrypt, set both for both.
type Options struct {
	// Level is the at-rest compression level. The zero value ([CompressionNone])
	// is no compression — pages are stored raw.
	Level Compression

	// TempDir is the directory in which the transient, uncompressed working
	// copy is created while the database is open. Empty uses the OS temp dir
	// (see [os.MkdirTemp]). The working copy holds the full uncompressed
	// database for the lifetime of the open handle.
	TempDir string

	// MaxInflatedSize, if > 0, caps the number of bytes [OpenSnapshot] will inflate
	// from the compressed file; inflation past it fails instead of filling the
	// disk. Leave 0 (unlimited) for a database you created. Set a sane upper
	// bound when opening a compressed file from an UNTRUSTED source — otherwise
	// a tiny crafted frame can inflate to an arbitrarily large working copy (a
	// decompression bomb).
	MaxInflatedSize int64

	// PageSize is the logical SQLite page size for the live compressing VFS
	// ([Open]/[NewVFS]); it is ignored by the snapshot [OpenSnapshot]. A large page
	// amortises the per-page directory overhead and widens the compression
	// window. Zero uses a 64 KiB default. It must be a power of two in
	// [512, 65536] and must equal the database's page_size — [Open] sets
	// both for you.
	PageSize int

	// BlockSize is the physical block granularity of the live container
	// ([Open]/[NewVFS]); it is ignored by the snapshot [OpenSnapshot]. Every
	// physical read and write is block-aligned, which also keeps the door open
	// for per-block encryption later. Zero uses a 4 KiB default. It must be a
	// power of two in [512, 65536] and must not exceed PageSize.
	BlockSize int

	// DirSegmentPages is the number of page-directory entries per on-disk
	// directory segment, set at create only and recorded in the container (so it
	// reads back regardless of the value configured later). A commit re-encodes
	// only the segments whose pages changed, so a smaller value bounds the
	// per-commit directory write at the cost of a larger segment index. Zero uses
	// a balanced default; it must be in [16, 1048576]. Ignored by [OpenSnapshot]
	// and when opening an existing container (its stored value wins).
	DirSegmentPages int

	// Key, if non-empty, encrypts the database at rest in the live compressing
	// VFS ([Open]/[NewVFS]). It is the raw cipher key — 32 bytes for the default
	// Adiantum cipher, 64 bytes for AES-XTS-256; derive one from a passphrase
	// with [gosqlite.org/vfs/crypto.DeriveKey]. Each compressed block is encrypted
	// (compress then encrypt), confidentiality at rest only (no integrity tag),
	// reusing the cipher of gosqlite.org/vfs/crypto. Empty leaves the database
	// unencrypted. Ignored by the snapshot [OpenSnapshot].
	Key []byte

	// Cipher selects the at-rest cipher when Key (or Recipients) is set. The zero
	// value is Adiantum (32-byte key); AES-XTS-256 needs a 64-byte key.
	Cipher crypto.Cipher

	// Recipients, if set, encrypts the database at rest to a random data key that
	// is wrapped for each recipient (an SSH key, a passphrase, etc.) — so several
	// parties can each open it with their own [Identities] without a shared
	// secret, the age model. Set at create only; mutually exclusive with Key.
	// Build recipients with gosqlite.org/crypto/keyring. Ignored by [OpenSnapshot].
	Recipients []keyring.Recipient

	// Identities unwraps the data key when opening a database created with
	// Recipients. The first identity that matches a keyslot opens it; none
	// matching is reported as ErrNoIdentity.
	Identities []keyring.Identity

	// Masters pins administrator keys (ed25519, via gosqlite.org/crypto/keyring):
	// only a master can add or remove recipients and masters (via [Rewrap] /
	// [Rekey]). At create it is the initial admin set (with SignWith). At open it
	// is the masters you TRUST to have authorized the membership — a keyslot not
	// signed by one of them is rejected with ErrUnauthorized; supply none to skip
	// that check and just read. Empty everywhere is the flat model, where any
	// recipient can administer. Set at create only.
	Masters []keyring.MasterRecipient

	// SignWith is the master identity that signs the keyslot at create. Required
	// when Masters is set, and must be one of them.
	SignWith keyring.MasterIdentity

	// Writers pins the writer keys (ed25519) allowed to modify the database in
	// authenticated mode: every commit is signed by a writer and verified against
	// this set, so a recipient that is NOT a writer is read-only. Setting Writers
	// turns on authenticated mode and requires Masters (an admin authorizes the
	// writer list); the writer list is administered thereafter by masters via
	// [Rewrap]/[Rekey]. At open, Masters is the trust anchor — the authorized
	// writers are taken from the master-signed keyslot — so a reader only pins
	// Masters. Set at create only.
	Writers []keyring.WriterRecipient

	// WriteAs is the writer identity that signs commits on this connection; it
	// must be one of Writers. Omit it to open an authenticated database read-only:
	// the VFS refuses writes (ErrReadOnlyRecipient).
	WriteAs keyring.WriterIdentity

	// Authenticate turns on tamper-evident storage with a symmetric root — without
	// ed25519 Writers. Every commit carries an HMAC over the committed state (keyed
	// by a key derived from the data key) and each slot a hash, so a modified or
	// partially-rolled-back container fails to open with [ErrTampered]. Requires
	// encryption (Key or Recipients), and protects against an attacker WITHOUT the
	// key. It is tamper-evident, not anti-replay: a complete, self-consistent earlier
	// committed image is still validly signed and opens — preventing a full rollback
	// needs a monotonic anchor outside the file; use [Rekey] for durable revocation.
	// For read-only recipients (where a holder of the read key must not be able to
	// forge a write), use Writers instead, which authenticates with a writer
	// signature. Set at create only.
	Authenticate bool

	// Anchor upgrades authenticated mode from tamper-evident to rollback-RESISTANT
	// with an external monotonic counter kept outside the database file (see
	// [ReplayAnchor]). Each commit records its generation in the anchor; opening a
	// database whose committed generation is below the recorded floor — a complete
	// but stale earlier image an attacker rolled back to — fails with
	// [ErrRolledBack]. Requires authenticated mode (Authenticate or Writers), since
	// the floor is meaningless without an authenticated generation. The anchor is
	// only as strong as its storage; see [ReplayAnchor] and [FileAnchor].
	Anchor ReplayAnchor
}

// resolveLive validates and defaults the live-VFS geometry, returning the
// physical block size and logical page size in bytes.
func (o Options) resolveLive() (blockSize, pageSize, segEntries uint64, err error) {
	ps := o.PageSize
	if ps == 0 {
		ps = defaultPageSize
	}
	bs := o.BlockSize
	if bs == 0 {
		bs = defaultBlockSize
	}
	se := o.DirSegmentPages
	if se == 0 {
		se = defaultSegEntries
	}
	if !isPow2InRange(ps) {
		return 0, 0, 0, fmt.Errorf("vault: invalid PageSize %d (want a power of two in [512, 65536])", ps)
	}
	if !isPow2InRange(bs) {
		return 0, 0, 0, fmt.Errorf("vault: invalid BlockSize %d (want a power of two in [512, 65536])", bs)
	}
	if bs > ps {
		return 0, 0, 0, fmt.Errorf("vault: BlockSize %d exceeds PageSize %d", bs, ps)
	}
	if se < 16 || se > 1<<20 {
		return 0, 0, 0, fmt.Errorf("vault: invalid DirSegmentPages %d (want [16, 1048576])", se)
	}
	return uint64(bs), uint64(ps), uint64(se), nil
}

// isPow2InRange reports whether n is a power of two within SQLite's valid page
// size range, which the block size also reuses.
func isPow2InRange(n int) bool { return n >= 512 && n <= 65536 && n&(n-1) == 0 }
