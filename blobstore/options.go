package blobstore

// DefaultChunkSize is the chunk size used for objects created by a Store
// opened without [WithChunkSize]. 64 KiB balances per-chunk overhead against
// the wasted tail of the final (partially filled) chunk.
const DefaultChunkSize = 64 << 10

// Option configures a [Store] at [Open] time.
type Option func(*Store)

// WithChunkSize sets the chunk size (in bytes) for objects this Store
// creates. It must be positive. The chunk size is frozen per object at
// [Store.Create] time, so changing a Store's chunk size never affects
// objects that already exist — they keep reading and writing at the size
// they were created with.
//
// Smaller chunks waste less space in the final partial chunk but add per-row
// overhead and more BLOB opens per large write; larger chunks are the
// reverse. The default ([DefaultChunkSize], 64 KiB) suits streamed file
// content.
func WithChunkSize(n int) Option {
	return func(s *Store) { s.chunkSize = n }
}

// Compression selects whether (and how hard) a Store compresses the objects
// it creates. Levels run from fastest (least reduction) to best (most), with
// the underlying codec abstracted away. Compression is frozen per object at
// [Store.Create] time; the zero value [CompressionNone] stores objects raw.
//
// Compression trades CPU and memory for storage: a compressed object can't use
// in-place incremental BLOB I/O, so a partial write read-modify-writes its
// whole chunk and a read decompresses the whole chunk. It fits write-once /
// read-mostly or sequentially-streamed compressible data (files, logs, JSON),
// not hot random partial updates or already-compressed payloads. Prefer a
// larger [WithChunkSize] when compressing.
type Compression int

const (
	CompressionNone    Compression = iota // store raw (default)
	CompressionFastest                    // lowest latency, least reduction
	CompressionFast
	CompressionDefault // balanced — the recommended setting when compressing
	CompressionBetter
	CompressionBest // most reduction, slowest
)

// WithCompression makes the Store store newly [Store.Create]d objects
// compressed at level c. Objects that already exist keep the mode they were
// created with (the mode is frozen per object); the Store reads any object
// regardless of its mode. Default is [CompressionNone].
func WithCompression(c Compression) Option {
	return func(s *Store) { s.compression = c }
}

// CreateOption customizes a single [Store.Create].
type CreateOption func(*createConfig)

type createConfig struct {
	compression Compression
	set         bool
}

// WithObjectCompression overrides the Store's [WithCompression] default for this
// one object at [Store.Create]: [CompressionNone] stores it raw, any level
// stores it compressed AT THAT LEVEL. Objects of different modes and levels
// coexist in one store — every read is mode- and level-agnostic — so a store can
// compress small or cold objects hard (e.g. [CompressionBest]) while keeping
// large or hot ones raw or lightly compressed for speed.
//
// The chosen level is frozen on the object: later writes use it regardless of
// the Store handle that performs them. Without this option an object inherits
// the Store's mode at Create, and its writes use the writing Store's level.
func WithObjectCompression(c Compression) CreateOption {
	return func(cc *createConfig) { cc.compression = c; cc.set = true }
}

// WithVacuumOnDelete makes [Store.Delete] and shrinking [Store.Truncate]
// issue PRAGMA incremental_vacuum after freeing chunks, returning the freed
// pages to the OS. This is a no-op unless the database is in incremental
// auto_vacuum mode — open it with
// Config.Pragmas.AutoVacuum = [gosqlite.org.AutoVacuumIncremental], or convert
// an existing one with [gosqlite.org.DB.SetAutoVacuum]. Off by default: freed
// pages are reused by SQLite for later writes without growing the file.
func WithVacuumOnDelete() Option {
	return func(s *Store) { s.vacuumOnDelete = true }
}
