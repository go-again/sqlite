package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DB wraps *sql.DB so the caller can `defer db.Close()` without
// thinking about VFS lifecycle. Embedded *sql.DB means every
// database/sql method works unchanged:
//
//	db, err := sqlite.Open(sqlite.Config{Path: "x.db"})
//	if err != nil { ... }
//	defer db.Close()
//
//	rows, err := db.Query("SELECT ...")  // *sql.DB methods
type DB struct {
	*sql.DB
	vfsCloser io.Closer // from Config.VFSCloser; closed after the pool drains
}

// Open is the modern Go-typed entry point. It builds a DSN that
// carries every requested PRAGMA as a `_pragma=` URL flag (so the
// driver applies them at connection open, on every connection in the
// pool — not just the one [database/sql] happens to dispatch the first
// Exec to), opens the underlying *sql.DB, eagerly Pings to surface
// any PRAGMA error as an Open error, and returns a wrapper whose Close
// also runs any [Config.VFSCloser].
//
// Lifecycle: a single defer db.Close() is sufficient — closing drains
// the *sql.DB pool first, then closes any Config.VFSCloser.
//
// Backward-compat note: this does not replace `sql.Open("sqlite",
// dsn)`. Both keep working. Open(Config) is the recommended new
// path; the DSN form is what BuildDSN produces, exposed for
// callers integrating with code that already speaks DSN.
func Open(cfg Config) (*DB, error) {
	if cfg.Path == "" {
		return nil, errors.New("sqlite: Config.Path is required")
	}

	dsn := buildDSN(cfg, cfg.VFS)

	sqlDB, err := sql.Open(DriverName, dsn)
	if err != nil {
		if cfg.VFSCloser != nil {
			_ = cfg.VFSCloser.Close()
		}
		return nil, fmt.Errorf("sqlite: sql.Open: %w", err)
	}

	if cfg.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	// Force an initial connection so a bad PRAGMA value (which would
	// otherwise be deferred to the first lazy connect) surfaces as an
	// Open error instead of a confusing first-query error later.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		if cfg.VFSCloser != nil {
			_ = cfg.VFSCloser.Close()
		}
		return nil, fmt.Errorf("sqlite: open first connection: %w", err)
	}

	return &DB{DB: sqlDB, vfsCloser: cfg.VFSCloser}, nil
}

// OpenInMemory is the shortest path to a private in-memory database:
// equivalent to [Open] with Config{Path: [InMemory]}. Convenient for
// tests, REPLs, and scratch usage that doesn't need PRAGMAs or
// pool tuning.
//
//	db, err := sqlite.OpenInMemory()
//	defer db.Close()
//
// NOT safe for a connection pool: each pooled connection gets its OWN
// private empty database, so a write made through one connection is invisible
// to the next, and pool-borrowing helpers (e.g. [gosqlite.org/blobstore])
// silently lose data. For an in-memory database shared across connections,
// use [OpenShared] — or pin the pool with SetMaxOpenConns(1).
func OpenInMemory() (*DB, error) {
	return Open(Config{Path: InMemory})
}

// OpenWAL opens a file-backed database with the [RecommendedPragmas]
// preset — WAL journaling, 5-second busy timeout, foreign keys
// enforced. Equivalent to:
//
//	sqlite.Open(sqlite.Config{Path: path, Pragmas: sqlite.RecommendedPragmas()})
//
// Most production applications want exactly this. Tune the pool size
// or other PRAGMAs by reaching for the full [Open]/[Config] form.
func OpenWAL(path string) (*DB, error) {
	return Open(Config{Path: path, Pragmas: RecommendedPragmas()})
}

// OpenReadOnly opens an existing file-backed database in read-only
// mode. Refuses to create the file if missing — matches SQLite's
// `mode=ro`. Equivalent to:
//
//	sqlite.Open(sqlite.Config{Path: path, Mode: sqlite.ModeReadOnly})
//
// Typical use: opening a shipped seed database or a read replica
// without risking accidental writes.
func OpenReadOnly(path string) (*DB, error) {
	return Open(Config{Path: path, Mode: ModeReadOnly})
}

// OpenShared opens (or creates) a named in-memory database that
// every connection in the same process pointing at the same name
// shares. Equivalent to:
//
//	sqlite.Open(sqlite.Config{Path: name, Mode: sqlite.ModeMemory, Cache: sqlite.CacheShared})
//
// Unlike [OpenInMemory] / [InMemory] (which gives each conn its own
// private store), OpenShared lets multiple *sql.DB handles see the
// same in-memory rows — the standard SQLite recipe for multi-conn
// in-memory tests. The shared store lives for the lifetime of the
// process; opening the same name from a second goroutine re-attaches
// to it.
//
// For snapshot isolation or richer semantics, use the
// [gosqlite.org/vfs/mvcc] or
// [gosqlite.org/vfs/memdb] sub-packages.
func OpenShared(name string) (*DB, error) {
	return Open(Config{Path: name, Mode: ModeMemory, Cache: CacheShared})
}

// Close drains the *sql.DB pool and then closes any [Config.VFSCloser]
// (a VFS-providing package's teardown). Order matters: pool first, VFS
// second. Idempotent.
func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	var errs []error
	if d.DB != nil {
		if err := d.DB.Close(); err != nil {
			errs = append(errs, fmt.Errorf("sqlite: close *sql.DB: %w", err))
		}
		d.DB = nil
	}
	if d.vfsCloser != nil {
		if err := d.vfsCloser.Close(); err != nil {
			errs = append(errs, fmt.Errorf("sqlite: close VFS: %w", err))
		}
		d.vfsCloser = nil
	}
	return errors.Join(errs...)
}

// PinConn borrows a single connection from the pool and returns the
// underlying *Conn together with a release func that returns it to the
// pool. The *Conn stays valid until release is called; do not use it
// afterward, and close any [Blob] (or other connection-scoped handle)
// opened on it BEFORE calling release.
//
// It removes the db.Conn + (*sql.Conn).Raw + type-assert boilerplate that
// every connection-scoped feature otherwise repeats — [Conn.OpenBlob], the
// SESSION/changeset extension, per-connection hooks, backup, serialize:
//
//	c, release, err := db.PinConn(ctx)
//	if err != nil { return err }
//	defer release()
//	blob, err := c.OpenBlob("main", "docs", "body", rowid, true)
//	// ... use blob, then blob.Close() before release runs ...
//
// Each pinned conn occupies one pool slot for its lifetime, so release
// promptly; holding more than [sql.DB.SetMaxOpenConns] at once deadlocks.
// For bulk byte-stream storage that manages this for you, see
// [gosqlite.org/blobstore].
func (d *DB) PinConn(ctx context.Context) (c *Conn, release func() error, err error) {
	sc, err := d.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := sc.Raw(func(driverConn any) error {
		cc, ok := driverConn.(*Conn)
		if !ok {
			return fmt.Errorf("sqlite: PinConn: unexpected driver conn %T", driverConn)
		}
		c = cc
		return nil
	}); err != nil {
		_ = sc.Close()
		return nil, nil, err
	}
	return c, sc.Close, nil
}

// IncrementalVacuum reclaims up to pages free pages from the database file,
// returning that space to the OS. Pass 0 to reclaim every free page. It is
// only effective when the database is in incremental auto_vacuum mode (open
// with Config.Pragmas.AutoVacuum = [AutoVacuumIncremental], or convert an
// existing database with [DB.SetAutoVacuum]); in any other mode it is a
// harmless no-op.
func (d *DB) IncrementalVacuum(ctx context.Context, pages int) error {
	stmt := "PRAGMA incremental_vacuum"
	if pages > 0 {
		stmt = fmt.Sprintf("PRAGMA incremental_vacuum(%d)", pages)
	}
	if _, err := d.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("sqlite: incremental_vacuum: %w", err)
	}
	return nil
}

// SetAutoVacuum changes the auto_vacuum mode of an EXISTING database and
// rewrites it with VACUUM so the new mode takes effect — the heavyweight
// conversion SQLite requires once a database has content. For a brand-new
// database prefer Config.Pragmas.AutoVacuum, which fixes the mode at creation
// with no VACUUM. mode must be [AutoVacuumNone], [AutoVacuumFull], or
// [AutoVacuumIncremental].
//
// The pragma and the VACUUM run on one pinned connection (the pending mode is
// connection-local until VACUUM persists it to the file header). VACUUM
// rewrites the whole database under an exclusive lock — call it deliberately
// as a maintenance step, never on every open.
func (d *DB) SetAutoVacuum(ctx context.Context, mode AutoVacuumMode) error {
	switch mode {
	case AutoVacuumNone, AutoVacuumFull, AutoVacuumIncremental:
	default:
		return fmt.Errorf("sqlite: SetAutoVacuum: invalid mode %q", mode)
	}
	sc, err := d.Conn(ctx)
	if err != nil {
		return err
	}
	defer sc.Close()
	if _, err := sc.ExecContext(ctx, "PRAGMA auto_vacuum = "+string(mode)); err != nil {
		return fmt.Errorf("sqlite: SetAutoVacuum: %w", err)
	}
	if _, err := sc.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("sqlite: SetAutoVacuum: VACUUM: %w", err)
	}
	return nil
}

// BuildDSN renders the full DSN that Open would build for the given
// Config, including every requested PRAGMA as a `_pragma=` URL flag.
// Exposed for callers that need the string form (e.g. wiring into a
// third-party tool that only accepts DSN strings):
//
//	dsn := sqlite.BuildDSN(cfg)
//	db, _ := sql.Open("sqlite", dsn)
//
// New code should prefer [Open] — it Pings to surface PRAGMA errors
// eagerly and bundles the [Config.VFSCloser] lifecycle. BuildDSN exists
// for the integration case.
//
// VFS teardown is NOT wired here — this is pure DSN rendering. Use [Open]
// if you set [Config.VFSCloser]; only it bundles that into db.Close().
func BuildDSN(cfg Config) string {
	return buildDSN(cfg, cfg.VFS)
}

// ApplyPragmas runs each requested PRAGMA against the given pool via
// *sql.DB.Exec. Exposed for callers who already have a `sql.Open(dsn)`-
// opened pool and want the typed PRAGMA surface without rebuilding the
// DSN:
//
//	db, _ := sql.Open("sqlite", "file:x.db")
//	sqlite.ApplyPragmas(db, sqlite.RecommendedPragmas())
//
// CAVEAT: this path is best-effort for per-connection PRAGMAs
// (busy_timeout, foreign_keys, cache_size, temp_store, synchronous):
// [database/sql] picks any idle connection per Exec, so the setting
// only sticks on whichever conn the Exec hit. For pool-wide
// correctness, use [Open] — it encodes every PRAGMA into the DSN so
// the driver applies them at each connection open. ApplyPragmas is
// safe for journal_mode (DB-file attribute, propagates) and for users
// running with MaxOpenConns=1.
//
// Iteration order is deterministic: declared fields first, then Extra
// keys in sorted order.
func ApplyPragmas(db *sql.DB, p Pragmas) error {
	for _, stmt := range pragmaStatements(p) {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("sqlite: %s: %w", stmt, err)
		}
	}
	return nil
}

// buildDSN renders the canonical DSN: `file:<escaped-path>?<query>`.
// Query carries mode=, vfs=, and one `_pragma=` per requested PRAGMA
// (declared fields, then Extra keys in sorted order).
//
// Path escaping handles `?`, `&`, `#`, space, and `%`. SQLite's URI
// parser percent-decodes the path before opening the file, so the
// round-trip is transparent. The `:memory:` magic path is preserved
// verbatim (colons are URI-safe in the path component).
//
// Mode rendering is skipped for the `:memory:` magic path: the legacy
// form is already in-memory, and appending `?mode=memory` produces
// the malformed `file::memory:?mode=memory`.
func buildDSN(cfg Config, vfsName string) string {
	var b strings.Builder
	b.WriteString("file:")
	b.WriteString(escapeDSNPath(cfg.Path))

	q := url.Values{}
	if cfg.Mode != "" && cfg.Path != InMemory {
		q.Set("mode", string(cfg.Mode))
	}
	if cfg.Cache != "" {
		q.Set("cache", string(cfg.Cache))
	}
	if vfsName != "" {
		q.Set("vfs", vfsName)
	}
	for _, p := range pragmaURLValues(cfg.Pragmas) {
		q.Add("_pragma", p)
	}
	if len(q) > 0 {
		b.WriteString("?")
		b.WriteString(q.Encode())
	}
	return b.String()
}

// escapeDSNPath percent-encodes the characters that would otherwise
// terminate the URI path or be reserved for the query / fragment.
// Other characters (including `/`, `:`, `.`, alphanumerics) pass
// through verbatim so common paths render exactly as the user wrote
// them.
func escapeDSNPath(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch c {
		case '%', '?', '&', '#', ' ':
			fmt.Fprintf(&b, "%%%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// pragmaPairs walks the typed Pragmas in canonical order (declared
// fields first, then Extra keys sorted) and calls emit for each set
// field. Empty / zero fields are skipped. The single ordered walk is
// the source of truth for both DSN encodings below, so a newly-added
// typed pragma can't end up in one encoding but not the other.
//
// fkOn is the rendering of a true ForeignKeys flag — "on" for the
// `_pragma()` URL form, "ON" for the `PRAGMA … = …` statement form —
// the only field whose canonical spelling differs between encodings.
func pragmaPairs(p Pragmas, fkOn string, emit func(name, value string)) {
	// auto_vacuum only takes hold on an empty database. What guarantees that
	// here is timing, not this emit order (the driver re-sorts _pragma values
	// before applying them): every Config pragma runs at connection open,
	// before any CREATE TABLE gives the database content. Emitted first anyway,
	// as the one creation-time pragma.
	if p.AutoVacuum != "" {
		emit(PragmaAutoVacuum, string(p.AutoVacuum))
	}
	if p.JournalMode != "" {
		emit(PragmaJournalMode, string(p.JournalMode))
	}
	if p.BusyTimeout > 0 {
		emit(PragmaBusyTimeout, fmt.Sprintf("%d", p.BusyTimeout.Milliseconds()))
	}
	if p.Synchronous != "" {
		emit(PragmaSynchronous, string(p.Synchronous))
	}
	if p.ForeignKeys {
		emit(PragmaForeignKeys, fkOn)
	}
	if p.CacheSize != 0 {
		emit(PragmaCacheSize, fmt.Sprintf("%d", p.CacheSize))
	}
	if p.TempStore != "" {
		emit(PragmaTempStore, string(p.TempStore))
	}
	if len(p.Extra) > 0 {
		keys := make([]string, 0, len(p.Extra))
		for k := range p.Extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			emit(k, p.Extra[k])
		}
	}
}

// pragmaURLValues returns the `_pragma=` URL values for the typed
// Pragmas, deterministically ordered. Used by [Open] and [BuildDSN] to
// encode PRAGMAs into the DSN.
func pragmaURLValues(p Pragmas) []string {
	var out []string
	pragmaPairs(p, "on", func(name, value string) {
		out = append(out, fmt.Sprintf("%s(%s)", name, value))
	})
	return out
}

// pragmaStatements renders the typed Pragmas as standalone
// `PRAGMA <name> = <value>` strings, deterministically ordered. Used
// by [ApplyPragmas] for the legacy `sql.Open(dsn)` path.
func pragmaStatements(p Pragmas) []string {
	var out []string
	pragmaPairs(p, "ON", func(name, value string) {
		out = append(out, fmt.Sprintf("PRAGMA %s = %s", name, value))
	})
	return out
}
