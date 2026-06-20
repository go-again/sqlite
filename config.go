package sqlite

import (
	"io"
	"time"
)

// InMemory is the canonical SQLite path for a private per-conn
// in-memory database — the literal string ":memory:". Use it instead
// of sprinkling the magic colon-form through your code:
//
//	db, _ := sql.Open("sqlite", sqlite.InMemory)
//	db, _ := sqlite.Open(sqlite.Config{Path: sqlite.InMemory})
//
// For a quicker no-Config entry, see [OpenInMemory]. For an in-memory
// database shared across multiple connections in the same process,
// pair an empty Path with one of the in-memory VFSes — see
// [gosqlite.org/vfs/memdb] / [gosqlite.org/vfs/mvcc].
const InMemory = ":memory:"

// Config is the modern, struct-typed way to describe a SQLite open.
// Every knob you'd otherwise stuff into a `file:…?_pragma=…&vfs=…`
// DSN string is a Go field here. Zero-valued fields leave SQLite at
// its own default; only the values you set fire as PRAGMA statements.
//
// Pass to [Open] to receive a [*DB] that bundles the *sql.DB pool
// with any VFS handles the open registered, so a single Close
// releases everything in the right order.
//
// The classic DSN-string entry points (`sql.Open("sqlite", dsn)`,
// `sql.Open("sqlite3", dsn)`) keep working unchanged. Config is the
// new way, not the only way.
type Config struct {
	// Path is the on-disk file path. Use [InMemory] (or the literal
	// ":memory:") for a private per-conn in-memory database. Required.
	Path string

	// Mode selects the open mode. Zero value is [ModeReadWriteCreate]
	// (create the file if missing, then read-write). Use the typed
	// constants below.
	Mode AccessMode

	// Pragmas are applied after open, in struct declaration order
	// followed by [Pragmas.Extra] entries. Use [RecommendedPragmas]
	// for the production preset (WAL + busy_timeout=5s +
	// foreign_keys=on).
	Pragmas Pragmas

	// VFS overrides the SQLite VFS name. Set only when you have a
	// pre-registered VFS to route through; leave empty otherwise.
	VFS string

	// VFSCloser, if set, is closed by [DB.Close] after the connection pool
	// drains. VFS-providing packages set it so a single db.Close() also
	// tears down the VFS they registered for this database — e.g.
	// [gosqlite.org/vfs/crypto]'s Open routes its cipher VFS through VFS and
	// its handle through VFSCloser. Leave nil otherwise.
	VFSCloser io.Closer

	// Cache routes the SQLite `cache=` URI parameter. Use [CacheShared]
	// to let multiple connections in the same process see the same
	// (possibly in-memory) database; leave empty for SQLite's default
	// per-conn private cache.
	Cache CacheMode

	// MaxOpenConns / MaxIdleConns / ConnMaxLifetime are passed
	// through to the underlying *sql.DB via the corresponding
	// SetMaxOpenConns / SetMaxIdleConns / SetConnMaxLifetime calls.
	// Zero = leave at sql defaults.
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// AccessMode is the typed enum for SQLite's `mode=` URI parameter.
// Zero value [ModeReadWriteCreate] matches the implicit behavior of
// a bare `file:…` DSN.
type AccessMode string

const (
	// ModeReadWriteCreate creates the file if missing, then opens
	// read-write. Default.
	ModeReadWriteCreate AccessMode = "rwc"
	// ModeReadWrite opens read-write but errors if the file is
	// missing.
	ModeReadWrite AccessMode = "rw"
	// ModeReadOnly opens read-only.
	ModeReadOnly AccessMode = "ro"
	// ModeMemory opens a private in-memory database (DSN form
	// "file:name?mode=memory"); equivalent to ":memory:" for most
	// callers.
	ModeMemory AccessMode = "memory"
)

// CacheMode is the typed enum for SQLite's `cache=` URI parameter.
// Empty value means "leave at SQLite's default" (per-conn private).
type CacheMode string

const (
	// CacheShared makes multiple connections to the same path share
	// a single page cache. Critical for multi-conn in-memory databases
	// (the pattern `file:name?mode=memory&cache=shared`) since without
	// it each conn would get its own private memory store. See
	// [OpenShared] for the shortcut.
	CacheShared CacheMode = "shared"
	// CachePrivate is the SQLite default — each connection has its
	// own page cache. Setting this explicitly is rarely useful; leave
	// empty for the same behavior.
	CachePrivate CacheMode = "private"
)

// JournalMode is the typed enum for SQLite's journal_mode pragma.
// Empty value leaves SQLite at its compile-time default (DELETE).
type JournalMode string

const (
	JournalDelete   JournalMode = "DELETE"
	JournalTruncate JournalMode = "TRUNCATE"
	JournalPersist  JournalMode = "PERSIST"
	JournalMemory   JournalMode = "MEMORY"
	JournalWAL      JournalMode = "WAL"
	JournalOff      JournalMode = "OFF"
)

// Synchronous is the typed enum for SQLite's synchronous pragma.
// Empty value leaves SQLite at its compile-time default (FULL).
type Synchronous string

const (
	SynchronousOff    Synchronous = "OFF"
	SynchronousNormal Synchronous = "NORMAL"
	SynchronousFull   Synchronous = "FULL"
	SynchronousExtra  Synchronous = "EXTRA"
)

// TempStore is the typed enum for SQLite's temp_store pragma.
// Empty value leaves SQLite at its compile-time default (DEFAULT,
// which usually resolves to FILE).
type TempStore string

const (
	TempStoreDefault TempStore = "DEFAULT"
	TempStoreFile    TempStore = "FILE"
	TempStoreMemory  TempStore = "MEMORY"
)

// AutoVacuumMode is the typed enum for SQLite's auto_vacuum pragma. Unlike
// most pragmas it is a database-creation-time property: it takes effect only
// on an empty database (before any table exists) or after a VACUUM. Empty
// value leaves SQLite at its default (NONE). See [Pragmas.AutoVacuum] for the
// new-database path and [DB.SetAutoVacuum] for converting an existing one.
type AutoVacuumMode string

const (
	// AutoVacuumNone disables auto-vacuum (SQLite default): freed pages
	// become a free list reused by later writes; the file never shrinks.
	AutoVacuumNone AutoVacuumMode = "NONE"
	// AutoVacuumFull returns freed pages to the OS automatically on every
	// commit.
	AutoVacuumFull AutoVacuumMode = "FULL"
	// AutoVacuumIncremental tracks freed pages but reclaims them only when
	// you call [DB.IncrementalVacuum].
	AutoVacuumIncremental AutoVacuumMode = "INCREMENTAL"
)

// Exported pragma-key constants. Used internally by the DSN renderer
// and the [ApplyPragmas] / [BuildDSN] code paths; exposed so callers
// building custom pragma strings or migration scripts can reference
// the canonical SQLite spelling without re-hardcoding it.
const (
	PragmaAutoVacuum  = "auto_vacuum"
	PragmaJournalMode = "journal_mode"
	PragmaBusyTimeout = "busy_timeout"
	PragmaSynchronous = "synchronous"
	PragmaForeignKeys = "foreign_keys"
	PragmaCacheSize   = "cache_size"
	PragmaTempStore   = "temp_store"
)

// Pragmas maps the most common SQLite knobs to Go fields. Unset
// values are "leave SQLite alone"; only the values you populate fire
// as PRAGMA statements at open time.
//
// For Pragmas not surfaced here (there are many), use [Pragmas.Extra].
type Pragmas struct {
	// AutoVacuum sets SQLite's auto_vacuum mode. The mode is fixed when the
	// database is created, so it only takes effect on a fresh database — which
	// works here because every Config pragma is applied at connection open,
	// before your first CREATE TABLE. Converting an existing, populated
	// database needs [DB.SetAutoVacuum]. Pair [AutoVacuumIncremental] with
	// [DB.IncrementalVacuum] to reclaim space on demand. Empty = leave at
	// SQLite's default (NONE).
	AutoVacuum AutoVacuumMode

	// JournalMode selects the journaling strategy. Use [JournalWAL]
	// for production. Empty = leave alone.
	JournalMode JournalMode

	// BusyTimeout is mapped onto `PRAGMA busy_timeout = <ms>`. Zero =
	// leave alone (SQLite default 0 ms = immediate SQLITE_BUSY).
	BusyTimeout time.Duration

	// Synchronous controls the durability/throughput tradeoff. Use
	// [SynchronousNormal] with WAL for typical production. Empty =
	// leave alone.
	Synchronous Synchronous

	// ForeignKeys enables foreign-key enforcement when true. SQLite
	// default is OFF; false here means "leave at default".
	ForeignKeys bool

	// CacheSize: positive = pages, negative = KiB, zero = leave alone.
	CacheSize int

	// TempStore selects where temporary tables and indices live. Use
	// [TempStoreMemory] to avoid disk-resident temp files. Empty =
	// leave alone.
	TempStore TempStore

	// Extra is the escape hatch for Pragmas not in the typed
	// surface. Each entry fires `PRAGMA <key> = <value>`.
	Extra map[string]string
}

// RecommendedPragmas returns a production-shaped preset: WAL
// journaling, 5-second busy timeout, foreign keys enforced.
// Most consumer applications want exactly this; tune for your
// workload as needed.
func RecommendedPragmas() Pragmas {
	return Pragmas{
		JournalMode: JournalWAL,
		BusyTimeout: 5 * time.Second,
		ForeignKeys: true,
	}
}
