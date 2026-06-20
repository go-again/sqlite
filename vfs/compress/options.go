package compress

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

// Options configures a compressed database opened with [Open].
type Options struct {
	// Level is the at-rest compression level. The zero value compresses at
	// the default level.
	Level Compression

	// TempDir is the directory in which the transient, uncompressed working
	// copy is created while the database is open. Empty uses the OS temp dir
	// (see [os.MkdirTemp]). The working copy holds the full uncompressed
	// database for the lifetime of the open handle.
	TempDir string
}
