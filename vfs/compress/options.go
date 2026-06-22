package compress

import (
	"fmt"

	"gosqlite.org/crypto/keyring"
	"gosqlite.org/vfs/crypto"
)

// Compression selects the at-rest compression level. It mirrors the level
// enum of [gosqlite.org/blobstore]. CompressionNone is not meaningful for a
// compressed database (use a plain [gosqlite.org.Open] for an uncompressed
// one), so it is treated as CompressionDefault.
type Compression int

const (
	// CompressionNone is the zero value; here it maps to CompressionDefault.
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

// Options configures a compressed database opened with [Open] or [OpenSnapshot].
type Options struct {
	// Level is the at-rest compression level. The zero value compresses at
	// the default level.
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
}

// resolveLive validates and defaults the live-VFS geometry, returning the
// physical block size and logical page size in bytes.
func (o Options) resolveLive() (blockSize, pageSize uint64, err error) {
	ps := o.PageSize
	if ps == 0 {
		ps = defaultPageSize
	}
	bs := o.BlockSize
	if bs == 0 {
		bs = defaultBlockSize
	}
	if !isPow2InRange(ps) {
		return 0, 0, fmt.Errorf("compress: invalid PageSize %d (want a power of two in [512, 65536])", ps)
	}
	if !isPow2InRange(bs) {
		return 0, 0, fmt.Errorf("compress: invalid BlockSize %d (want a power of two in [512, 65536])", bs)
	}
	if bs > ps {
		return 0, 0, fmt.Errorf("compress: BlockSize %d exceeds PageSize %d", bs, ps)
	}
	return uint64(bs), uint64(ps), nil
}

// isPow2InRange reports whether n is a power of two within SQLite's valid page
// size range, which the block size also reuses.
func isPow2InRange(n int) bool { return n >= 512 && n <= 65536 && n&(n-1) == 0 }
