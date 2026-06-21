package compress

import "fmt"

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
