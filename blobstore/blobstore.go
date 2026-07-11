package blobstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	sqlite "gosqlite.org"
	"gosqlite.org/internal/sqlid"
)

// ErrNotFound is returned (wrapped) by operations that name an object id not
// present in the store. Test for it with [errors.Is].
var ErrNotFound = errors.New("blobstore: object not found")

// ErrReadOnly is returned (wrapped) by every mutating operation on a Store
// opened with [OpenReadOnly]. Test for it with [errors.Is].
var ErrReadOnly = errors.New("blobstore: store is read-only")

// Store is a managed collection of growable byte objects backed by a handful of
// SQLite tables. Open it once with [Open] and share it; its methods are safe
// for concurrent use. A Store does not own the [gosqlite.org.DB] — closing
// the DB is the caller's job and invalidates the Store.
type Store struct {
	db *sqlite.DB

	// objs/chunks/blocks/versions are double-quoted identifiers spliced into
	// SQL; blocksRaw is the bare block-table name for OpenBlob, which takes an
	// identifier argument rather than SQL text. name is the raw store name. A
	// chunk's bytes live in a refcounted row of the blocks table; chunks is the
	// (obj, seq) -> block mapping, so two chunks (or two objects) can share one
	// block until one of them is written.
	name      string
	objs      string
	chunks    string
	blocks    string
	blocksRaw string
	versions  string

	chunkSize      int
	vacuumOnDelete bool
	compression    Compression
	dedup          bool
	readOnly       bool

	// now is the clock used for version timestamps; tests override it.
	now func() time.Time
}

// maxChunkSize bounds a stored chunk size. A chunk is held whole in memory, so
// this caps the largest single allocation a read or convert can be driven to
// make, guarding against a foreign/corrupt objects row (our own CHECK only
// enforces chunk > 0). It also keeps int(chunk) lossless on 32-bit builds.
const maxChunkSize = 1 << 30 // 1 GiB

// errCorruptChunk reports an object row whose chunk size is out of range.
// Create only ever writes a validated size, so this is reachable only if
// something outside this package wrote the row — we surface it as an error
// instead of dividing by zero or attempting an absurd allocation.
func errCorruptChunk(id, chunk int64) error {
	return fmt.Errorf("blobstore: object %d: invalid chunk size %d", id, chunk)
}

// checkChunk validates a chunk size read back from the objects table.
func checkChunk(id, chunk int64) error {
	if chunk <= 0 || chunk > maxChunkSize {
		return errCorruptChunk(id, chunk)
	}
	return nil
}

// Open prepares a Store backed by three tables derived from name:
// "<name>_objects" (object metadata), "<name>_blocks" (the refcounted chunk
// bytes), and "<name>_chunks" (the (obj, seq) -> block mapping). It creates them
// if they do not exist, so it is safe to call on every startup. name must be a
// valid SQL identifier (letters, digits, underscore; not starting with a digit).
//
// The same db (and the same name) may be reopened across process restarts to
// reattach to existing objects.
func Open(db *sqlite.DB, name string, opts ...Option) (*Store, error) {
	s, err := prepareStore(db, name, opts...)
	if err != nil {
		return nil, err
	}
	if err := s.migrate(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// OpenReadOnly reattaches to an already-provisioned store WITHOUT issuing any
// DDL: it creates no tables and runs no migration, so it works against a
// read-only database (snapshot browsing, an image on read-only media). It
// errors if the store's tables are not already present — open it writable once
// with [Open] first. Every mutating method on the returned Store returns
// [ErrReadOnly]; reads behave exactly as on a writable handle.
func OpenReadOnly(db *sqlite.DB, name string) (*Store, error) {
	s, err := prepareStore(db, name)
	if err != nil {
		return nil, err
	}
	s.readOnly = true
	if err := s.requireProvisioned(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// prepareStore builds and validates a Store from name + options but issues no
// DDL; Open layers migration on top, OpenReadOnly a provisioning probe.
func prepareStore(db *sqlite.DB, name string, opts ...Option) (*Store, error) {
	if db == nil {
		return nil, errors.New("blobstore: nil db")
	}
	if !sqlid.ValidIdent(name) {
		return nil, fmt.Errorf("blobstore: invalid name %q (want a SQL identifier)", name)
	}
	s := &Store{
		db:        db,
		name:      name,
		objs:      sqlid.QuoteIdent(name + "_objects"),
		chunks:    sqlid.QuoteIdent(name + "_chunks"),
		blocks:    sqlid.QuoteIdent(name + "_blocks"),
		blocksRaw: name + "_blocks",
		versions:  sqlid.QuoteIdent(name + "_versions"),
		chunkSize: DefaultChunkSize,
		now:       time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	if s.chunkSize <= 0 || s.chunkSize > maxChunkSize {
		return nil, fmt.Errorf("blobstore: chunk size must be in 1..%d bytes", maxChunkSize)
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		// keep_versions/max_age hold the per-object retention policy ([Policy]);
		// 0 means unlimited / no age bound.
		`CREATE TABLE IF NOT EXISTS ` + s.objs + ` (` +
			`id INTEGER PRIMARY KEY, size INTEGER NOT NULL DEFAULT 0, ` +
			`chunk INTEGER NOT NULL CHECK(chunk > 0), codec INTEGER NOT NULL DEFAULT 0, ` +
			`level INTEGER NOT NULL DEFAULT 0, ` +
			`keep_versions INTEGER NOT NULL DEFAULT 0, max_age INTEGER NOT NULL DEFAULT 0)`,
		// blocks holds the chunk bytes once, refcounted: refs counts the chunk
		// mappings pointing at the row, so a block shared by a clone is copied on
		// write and freed when the last reference goes. enc records the byte
		// encoding (verbatim or compressed) and is the reserved home for a future
		// per-block encryption flag. hash is the content hash used by the opt-in
		// dedup path ([WithDedup]); NULL for blocks written without dedup.
		`CREATE TABLE IF NOT EXISTS ` + s.blocks + ` (` +
			`id INTEGER PRIMARY KEY, data BLOB NOT NULL, ` +
			`enc INTEGER NOT NULL DEFAULT 0, refs INTEGER NOT NULL DEFAULT 1, hash BLOB)`,
		// chunks maps (obj, seq) to the block holding its bytes. WITHOUT ROWID:
		// the table is a pure covering index, no second rowid to carry.
		`CREATE TABLE IF NOT EXISTS ` + s.chunks + ` (` +
			`obj INTEGER NOT NULL, seq INTEGER NOT NULL, ` +
			`block INTEGER NOT NULL, PRIMARY KEY(obj, seq)) WITHOUT ROWID`,
		// versions records point-in-time snapshots: snapshot_obj is a hidden
		// clone object holding that version's chunk mapping (copy-on-write, so it
		// shares bytes with the live object until divergence).
		`CREATE TABLE IF NOT EXISTS ` + s.versions + ` (` +
			`id INTEGER PRIMARY KEY, obj INTEGER NOT NULL, version_no INTEGER NOT NULL, ` +
			`created_at INTEGER NOT NULL, snapshot_obj INTEGER NOT NULL, label TEXT, ` +
			`UNIQUE(obj, version_no))`,
		// Partial unique index backing dedup: only hashed (deduped) blocks are
		// indexed, so a non-dedup store pays nothing for it.
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + sqlid.QuoteIdent(s.name+"_blocks_hash") +
			` ON ` + s.blocks + ` (hash) WHERE hash IS NOT NULL`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("blobstore: migrate: %w", err)
		}
	}
	return nil
}

// requireProvisioned errors unless the store's core tables already exist, so
// OpenReadOnly fails cleanly on an un-provisioned (or misnamed) store rather
// than later, mid-read, with an opaque "no such table".
func (s *Store) requireProvisioned(ctx context.Context) error {
	for _, tbl := range []string{s.name + "_objects", s.name + "_blocks", s.name + "_chunks", s.name + "_versions"} {
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl).Scan(&n); err != nil {
			return fmt.Errorf("blobstore: OpenReadOnly %q: %w", s.name, err)
		}
		if n == 0 {
			return fmt.Errorf("blobstore: %q is not provisioned (no %s table); open it writable once with Open", s.name, tbl)
		}
	}
	return nil
}

// Create inserts a new, empty object and returns its id. Its storage MODE (raw
// or compressed) and compression LEVEL are initialized here from the Store's
// [WithCompression] setting or a per-object [WithObjectCompression] override.
// Both stay mutable: change them later with [Store.SetCompression], which
// rewrites the object's existing chunks when the mode changes. (The chunk size,
// by contrast, is fixed at Create.)
func (s *Store) Create(ctx context.Context, opts ...CreateOption) (int64, error) {
	if s.readOnly {
		return 0, fmt.Errorf("blobstore: create: %w", ErrReadOnly)
	}
	return s.createOn(ctx, s.db, opts...)
}

// createOn inserts a new empty object via ex — the pool, or a caller-pinned
// connection (via [Store.OnConn]) so the create joins the caller's transaction —
// and returns its id. A single INSERT needs no transaction of its own.
func (s *Store) createOn(ctx context.Context, ex execer, opts ...CreateOption) (int64, error) {
	var cc createConfig
	for _, o := range opts {
		o(&cc)
	}
	comp := s.compression
	level := 0 // 0 = no per-object override; writes use the Store's level
	if cc.set {
		comp = cc.compression
		if _, ok := comp.azLevel(); ok {
			level = int(comp) // freeze the override level on this object
		}
	}
	codec := codecRaw
	if _, ok := comp.azLevel(); ok {
		codec = codecAZ
	}
	res, err := ex.ExecContext(ctx,
		`INSERT INTO `+s.objs+` (size, chunk, codec, level, keep_versions, max_age) VALUES (0, ?, ?, ?, ?, ?)`,
		s.chunkSize, codec, level, cc.keepVersions, int64(cc.maxAge))
	if err != nil {
		return 0, fmt.Errorf("blobstore: create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("blobstore: create: %w", err)
	}
	return id, nil
}

// SetCompression sets object id's storage to compression c, converting the
// object when the storage mode must change. A compressing level makes the
// object compressed at that level; [CompressionNone] makes it raw.
//
// Changing only the LEVEL of an already-compressed object rewrites nothing:
// reads are level-agnostic, so already-written chunks keep their bytes and only
// future writes use the new level. An object may therefore hold chunks
// compressed at different levels (e.g. a small head at [CompressionBest], a
// large appended tail at [CompressionDefault]).
//
// Changing the MODE (raw↔compressed) rewrites every existing chunk into the new
// representation in one transaction — an O(object size) pass, so the object is
// never left half-converted. Use it to compress an object first stored raw, or
// to decompress one back to in-place random I/O. Setting the mode an object
// already has only records the level (a no-op for a raw object). Content is
// preserved either way.
func (s *Store) SetCompression(ctx context.Context, id int64, c Compression) error {
	return s.withTx(ctx, func(sc *sql.Conn) error {
		var chunk, codec, level int64
		err := sc.QueryRowContext(ctx,
			`SELECT chunk, codec, level FROM `+s.objs+` WHERE id = ?`, id).Scan(&chunk, &codec, &level)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("blobstore: SetCompression %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("blobstore: SetCompression %d: %w", id, err)
		}
		if err := checkChunk(id, chunk); err != nil {
			return err
		}

		fromCompressed := codec == codecAZ
		_, toCompressed := c.azLevel()

		if fromCompressed == toCompressed {
			// Mode unchanged. Record the level — the only thing that can differ;
			// it is unused (so stored as 0) for a raw object.
			newLevel := 0
			if toCompressed {
				newLevel = int(c)
			}
			if _, err := sc.ExecContext(ctx,
				`UPDATE `+s.objs+` SET level = ? WHERE id = ?`, newLevel, id); err != nil {
				return fmt.Errorf("blobstore: SetCompression %d: %w", id, err)
			}
			return nil
		}

		// Mode change: rewrite every existing chunk, then flip codec + level.
		if err := s.convertChunks(ctx, sc, id, chunk, fromCompressed, toCompressed, c); err != nil {
			return fmt.Errorf("blobstore: SetCompression %d: %w", id, err)
		}
		newCodec, newLevel := codecRaw, 0
		if toCompressed {
			newCodec, newLevel = codecAZ, int(c)
		}
		if _, err := sc.ExecContext(ctx,
			`UPDATE `+s.objs+` SET codec = ?, level = ? WHERE id = ?`, newCodec, newLevel, id); err != nil {
			return fmt.Errorf("blobstore: SetCompression %d: %w", id, err)
		}
		return nil
	})
}

// convertChunks rewrites every existing chunk of object id from its current
// storage mode into the target mode (toCompressed), reading each chunk's full
// plaintext and writing it back in the new representation. It walks the chunk
// seqs in order via the (obj, seq) index — O(1) extra memory regardless of
// object size — and leaves sparse holes (absent rows) untouched, since a hole
// reads as zeros in either mode. The caller holds an open write transaction on
// sc; each rewrite is a fresh statement, so no cursor stays open across a write.
func (s *Store) convertChunks(ctx context.Context, sc *sql.Conn, id, chunk int64, fromCompressed, toCompressed bool, lvl Compression) error {
	var seq int64 = -1
	for {
		var next int64
		err := sc.QueryRowContext(ctx,
			`SELECT seq FROM `+s.chunks+` WHERE obj = ? AND seq > ? ORDER BY seq LIMIT 1`, id, seq).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		plain, err := s.readChunkPlain(ctx, sc, id, next, chunk, fromCompressed)
		if err != nil {
			return err
		}
		if toCompressed {
			err = s.chunkPutCompressed(ctx, sc, id, next, plain, lvl)
		} else {
			err = s.chunkPutRaw(ctx, sc, id, next, plain)
		}
		if err != nil {
			return err
		}
		seq = next
	}
}

// readChunkPlain returns the full chunk-size plaintext of the existing chunk
// (id, seq), reading it per the source mode. The row must exist (the caller
// only passes seqs it just found).
func (s *Store) readChunkPlain(ctx context.Context, sc *sql.Conn, id, seq, chunk int64, fromCompressed bool) ([]byte, error) {
	if fromCompressed {
		plain, ok, err := s.chunkGetCompressed(ctx, sc, id, seq, chunk)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("blobstore: object %d chunk %d vanished during convert", id, seq)
		}
		return plain, nil
	}
	// Raw chunk: the block holds the plaintext itself (a chunk-size blob).
	var data []byte
	if err := sc.QueryRowContext(ctx,
		`SELECT b.data FROM `+s.chunks+` c JOIN `+s.blocks+` b ON c.block = b.id `+
			`WHERE c.obj = ? AND c.seq = ?`, id, seq).Scan(&data); err != nil {
		return nil, err
	}
	return data, nil
}

// ObjectInfo is an object's storage metadata, including its actual at-rest
// compression ratio (computed from the stored block sizes, not maintained).
type ObjectInfo struct {
	Size        int64       // logical (uncompressed) length in bytes
	StoredBytes int64       // bytes the object's blocks occupy on disk (UniqueBytes + SharedBytes)
	UniqueBytes int64       // bytes in blocks referenced only by this object (freed if it is deleted)
	SharedBytes int64       // bytes in blocks this object shares with clones/versions (kept by those)
	Ratio       float64     // StoredBytes/Size (0 when Size==0); below 1 means compressed or sparse
	ChunkSize   int64       // the object's chunk size
	Compressed  bool        // whether the object is stored compressed
	Level       Compression // compression level for future writes (0 = none / Store's level)
}

// Stat returns object id's storage metadata. The stored size is computed on
// demand from the block sizes, split into UniqueBytes (blocks this object alone
// references) and SharedBytes (blocks it shares with a clone or version) by the
// blocks' refcounts — so du-style reporting stays accurate without a maintained
// column even after [Store.Clone].
func (s *Store) Stat(ctx context.Context, id int64) (ObjectInfo, error) {
	var info ObjectInfo
	var codec, level int64
	// Group the object's chunks by block so each distinct block is counted once
	// (a block may back several chunks under dedup); cnt is how many of THIS
	// object's chunks reference it. refs == cnt means no other object/version
	// references the block — it is unique to this object and freed on delete;
	// refs > cnt means it is shared.
	grouped := ` FROM (SELECT block, count(*) AS cnt FROM ` + s.chunks + ` WHERE obj = ` + s.objs +
		`.id GROUP BY block) m JOIN ` + s.blocks + ` b ON b.id = m.block)`
	err := s.db.QueryRowContext(ctx,
		`SELECT size, chunk, codec, level, `+
			`(SELECT coalesce(sum(CASE WHEN b.refs = m.cnt THEN length(b.data) ELSE 0 END), 0)`+grouped+`, `+
			`(SELECT coalesce(sum(CASE WHEN b.refs > m.cnt THEN length(b.data) ELSE 0 END), 0)`+grouped+` `+
			`FROM `+s.objs+` WHERE id = ?`, id).
		Scan(&info.Size, &info.ChunkSize, &codec, &level, &info.UniqueBytes, &info.SharedBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return ObjectInfo{}, fmt.Errorf("blobstore: Stat %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("blobstore: Stat %d: %w", id, err)
	}
	info.StoredBytes = info.UniqueBytes + info.SharedBytes
	info.Compressed = codec == codecAZ
	info.Level = Compression(level)
	if info.Size > 0 {
		info.Ratio = float64(info.StoredBytes) / float64(info.Size)
	}
	return info, nil
}

// List returns the ids of every live object in the store, in ascending id order
// (empty if the store has none). It enables an external referential sweep — e.g.
// deleting objects that no application row still points at — without the caller
// having to track ids out of band. Pair with [Store.Size]/[Store.Stat] for sizes.
func (s *Store) List(ctx context.Context) ([]int64, error) {
	if err := s.requireProvisioned(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM `+s.objs+` ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("blobstore: List: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("blobstore: List: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("blobstore: List: %w", err)
	}
	return ids, nil
}

// Size reports the logical length in bytes of object id.
func (s *Store) Size(ctx context.Context, id int64) (int64, error) {
	return s.sizeOn(ctx, s.db, id)
}

// sizeOn reads object id's logical size from q — the pool, or a caller-pinned
// connection (via [Store.OnConn]) so it observes that transaction's own
// uncommitted writes.
func (s *Store) sizeOn(ctx context.Context, q queryRower, id int64) (int64, error) {
	var size int64
	err := q.QueryRowContext(ctx,
		`SELECT size FROM `+s.objs+` WHERE id = ?`, id).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("blobstore: size %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("blobstore: size %d: %w", id, err)
	}
	return size, nil
}

// Delete removes object id, frees the blocks it alone holds (blocks shared with
// a clone or a version survive, held by those references), and removes its
// versions and their snapshots. Deleting an id that does not exist returns
// ErrNotFound. If the Store was opened with [WithVacuumOnDelete] and the database
// is in incremental auto_vacuum mode, the freed pages are returned to the OS.
func (s *Store) Delete(ctx context.Context, id int64) error {
	if err := s.withTx(ctx, func(sc *sql.Conn) error {
		return s.deleteOnConn(ctx, sc, id)
	}); err != nil {
		return err
	}
	s.maybeVacuum(ctx)
	return nil
}

// deleteOnConn removes object id and its versions on the in-transaction conn sc.
// The caller owns the transaction; the post-delete vacuum (pool path) is the
// caller's concern under [Store.OnConn] (incremental_vacuum cannot run inside the
// caller's open transaction).
func (s *Store) deleteOnConn(ctx context.Context, sc *sql.Conn, id int64) error {
	existed, err := s.deleteObjectTx(ctx, sc, id)
	if err != nil {
		return fmt.Errorf("blobstore: delete %d: %w", id, err)
	}
	if !existed {
		return fmt.Errorf("blobstore: delete %d: %w", id, ErrNotFound)
	}
	// Cascade to the object's versions so deleting it never orphans the hidden
	// snapshot clones (which would hold block references forever).
	if err := s.deleteVersionsTx(ctx, sc, id); err != nil {
		return fmt.Errorf("blobstore: delete %d: %w", id, err)
	}
	return nil
}

// deleteObjectTx removes object id and releases the blocks its chunks hold,
// within the already-open transaction sc, reporting whether the object existed.
// It is shared by the public Delete and version pruning, which deletes a
// version's hidden snapshot clone the same way.
func (s *Store) deleteObjectTx(ctx context.Context, sc *sql.Conn, id int64) (bool, error) {
	res, err := sc.ExecContext(ctx, `DELETE FROM `+s.objs+` WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	return true, s.releaseChunks(ctx, sc, id, 0, false)
}

// maybeVacuum runs PRAGMA incremental_vacuum (best effort) when the Store was
// opened with [WithVacuumOnDelete]. It is a no-op unless the database is in
// incremental auto_vacuum mode. Run after the freeing transaction commits.
func (s *Store) maybeVacuum(ctx context.Context) {
	if s.vacuumOnDelete {
		_, _ = s.db.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	}
}

// Truncate sets object id to exactly size bytes. Shrinking deletes the chunks
// beyond the new end and zeroes any trailing bytes inside the boundary chunk;
// growing is sparse — the gap reads back as zeros until written.
func (s *Store) Truncate(ctx context.Context, id, size int64) error {
	if size < 0 {
		return errors.New("blobstore: truncate: negative size")
	}
	var cur int64 // old size, captured for the post-commit vacuum decision
	if err := s.withTx(ctx, func(sc *sql.Conn) error {
		var e error
		cur, e = s.truncateOnConn(ctx, sc, id, size)
		return e
	}); err != nil {
		return err
	}
	if size < cur {
		s.maybeVacuum(ctx)
	}
	return nil
}

// truncateOnConn sets object id to exactly size bytes on the in-transaction conn
// sc and returns the object's prior size (for the caller's vacuum decision). The
// caller owns the transaction.
func (s *Store) truncateOnConn(ctx context.Context, sc *sql.Conn, id, size int64) (int64, error) {
	var cur, chunk, codec, level int64
	err := sc.QueryRowContext(ctx,
		`SELECT size, chunk, codec, level FROM `+s.objs+` WHERE id = ?`, id).Scan(&cur, &chunk, &codec, &level)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("blobstore: truncate %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("blobstore: truncate %d: %w", id, err)
	}
	if err := checkChunk(id, chunk); err != nil {
		return 0, err
	}
	if size < cur {
		// Chunks whose first byte is at or past the new size are gone.
		firstDead := (size + chunk - 1) / chunk // ceil(size/chunk)
		if err := s.releaseChunks(ctx, sc, id, firstDead, true); err != nil {
			return 0, fmt.Errorf("blobstore: truncate %d: %w", id, err)
		}
		// Zero the live tail of the boundary chunk so a later re-grow reads zeros
		// there rather than resurrecting old bytes.
		if rem := size % chunk; rem != 0 {
			if err := s.zeroChunkTail(ctx, sc, id, size/chunk, rem, chunk, codec == codecAZ, Compression(level)); err != nil {
				return 0, fmt.Errorf("blobstore: truncate %d: %w", id, err)
			}
		}
	}
	if _, err := sc.ExecContext(ctx,
		`UPDATE `+s.objs+` SET size = ? WHERE id = ?`, size, id); err != nil {
		return 0, fmt.Errorf("blobstore: truncate %d: %w", id, err)
	}
	return cur, nil
}

// zeroChunkTail zeroes bytes [from:chunk) of the chunk at (id, seq) if it
// exists. Caller holds an open transaction on sc.
func (s *Store) zeroChunkTail(ctx context.Context, sc *sql.Conn, id, seq, from, chunk int64, compressed bool, objLevel Compression) error {
	if compressed {
		plain, ok, err := s.chunkGetCompressed(ctx, sc, id, seq, chunk)
		if err != nil || !ok {
			return err
		}
		clear(plain[from:])
		return s.chunkPutCompressed(ctx, sc, id, seq, plain, objLevel)
	}
	// A sparse boundary chunk already reads as zeros — don't materialize it.
	if _, ok, err := s.blockOf(ctx, sc, id, seq); err != nil || !ok {
		return err
	}
	block, err := s.privateRawBlock(ctx, sc, id, seq, chunk)
	if err != nil {
		return err
	}
	zeros := make([]byte, chunk-from)
	return s.blobWrite(sc, block, zeros, from)
}

// chunkGetCompressed returns the full chunk-size plaintext of (id, seq), or
// (nil, false) if the chunk is absent (a sparse hole the caller zero-fills). It
// reads the bytes from the block the chunk maps to.
func (s *Store) chunkGetCompressed(ctx context.Context, sc *sql.Conn, id, seq, chunk int64) ([]byte, bool, error) {
	var data []byte
	var enc int
	err := sc.QueryRowContext(ctx,
		`SELECT b.data, b.enc FROM `+s.chunks+` c JOIN `+s.blocks+` b ON c.block = b.id `+
			`WHERE c.obj = ? AND c.seq = ?`, id, seq).Scan(&data, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	plain, err := decodeChunk(data, enc, int(chunk))
	if err != nil {
		return nil, false, fmt.Errorf("blobstore: object %d chunk %d: %w", id, seq, err)
	}
	if int64(len(plain)) != chunk {
		return nil, false, fmt.Errorf("blobstore: object %d chunk %d: decoded %d bytes, want %d", id, seq, len(plain), chunk)
	}
	return plain, true, nil
}

// chunkPutCompressed stores plain (length == chunk) as chunk (id, seq),
// compressed at the object's frozen level (objLevel) or, if it has none, the
// Store's. Replaces the chunk's block content (copy-on-write if shared).
func (s *Store) chunkPutCompressed(ctx context.Context, sc *sql.Conn, id, seq int64, plain []byte, objLevel Compression) error {
	data, enc, err := encodeChunk(plain, writeLevel(objLevel, s.compression))
	if err != nil {
		return err
	}
	return s.putBlock(ctx, sc, id, seq, data, enc)
}

// chunkPutRaw stores plain (length == chunk) as the raw chunk (id, seq): the
// stored value is the plaintext itself — a chunk-size blob OpenBlob can address
// in place — with enc verbatim. Used when converting an object to raw mode; the
// normal raw write path allocates with zeroblob and writes in place instead.
func (s *Store) chunkPutRaw(ctx context.Context, sc *sql.Conn, id, seq int64, plain []byte) error {
	return s.putBlock(ctx, sc, id, seq, plain, encVerbatim)
}

// putBlock replaces the whole content of chunk (id, seq) with (data, enc),
// copy-on-write aware: a sparse chunk gets a fresh block; a chunk whose block is
// private (refs == 1) is updated in place; a chunk whose block is shared
// (refs > 1) is repointed at a new private block and the old block loses a ref.
func (s *Store) putBlock(ctx context.Context, sc *sql.Conn, id, seq int64, data []byte, enc int) error {
	if s.dedup {
		return s.putBlockDedup(ctx, sc, id, seq, data, enc)
	}
	cur, ok, err := s.blockOf(ctx, sc, id, seq)
	if err != nil {
		return err
	}
	if !ok {
		block, err := s.newBlock(ctx, sc, data, enc)
		if err != nil {
			return err
		}
		return s.mapChunk(ctx, sc, id, seq, block)
	}
	refs, err := s.blockRefs(ctx, sc, cur)
	if err != nil {
		return err
	}
	if refs <= 1 {
		// Mutating in place: also clear any content hash (a non-dedup store never
		// sets one, but a block hashed by an earlier dedup-on session must leave
		// the index when its bytes change, so a stale hash can never be matched).
		_, err := sc.ExecContext(ctx,
			`UPDATE `+s.blocks+` SET data = ?, enc = ?, hash = NULL WHERE id = ?`, data, enc, cur)
		return err
	}
	block, err := s.newBlock(ctx, sc, data, enc)
	if err != nil {
		return err
	}
	if err := s.mapChunk(ctx, sc, id, seq, block); err != nil {
		return err
	}
	return s.decBlockRef(ctx, sc, cur)
}

// privateRawBlock returns the id of a block that chunk (id, seq) may be mutated
// in place through, copy-on-write aware: a sparse chunk gets a fresh
// zeroblob(chunk) block; a private block (refs == 1) is returned as-is; a shared
// block (refs > 1) is copied, the chunk repointed at the copy, and the original
// loses a ref. The returned block always has refs == 1.
func (s *Store) privateRawBlock(ctx context.Context, sc *sql.Conn, id, seq, chunk int64) (int64, error) {
	cur, ok, err := s.blockOf(ctx, sc, id, seq)
	if err != nil {
		return 0, err
	}
	if !ok {
		res, err := sc.ExecContext(ctx,
			`INSERT INTO `+s.blocks+` (data) VALUES (zeroblob(?))`, chunk)
		if err != nil {
			return 0, err
		}
		block, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		return block, s.mapChunk(ctx, sc, id, seq, block)
	}
	refs, err := s.blockRefs(ctx, sc, cur)
	if err != nil {
		return 0, err
	}
	if refs <= 1 {
		// The block is about to be mutated in place. If dedup gave it a content
		// hash, drop the hash first so the index can never match a block whose
		// bytes are about to change underneath it.
		if s.dedup {
			if _, err := sc.ExecContext(ctx,
				`UPDATE `+s.blocks+` SET hash = NULL WHERE id = ? AND hash IS NOT NULL`, cur); err != nil {
				return 0, err
			}
		}
		return cur, nil
	}
	block, err := s.copyBlock(ctx, sc, cur)
	if err != nil {
		return 0, err
	}
	if err := s.mapChunk(ctx, sc, id, seq, block); err != nil {
		return 0, err
	}
	return block, s.decBlockRef(ctx, sc, cur)
}

// releaseChunks drops object id's chunk mappings — all of them, or only those
// with seq >= fromSeq when ranged — decrementing each referenced block's refs by
// the number of those mappings that point at it and freeing any block whose last
// reference is gone (a block still shared with a clone or version survives). The
// decrement is by per-block multiplicity, not by one, so a block referenced by
// several of the object's chunks (possible under dedup) settles correctly. It is
// the shared teardown for Delete and a shrinking Truncate; the mappings still
// exist while the blocks are touched, so the work stays scoped to this object
// via the (obj, seq) index.
func (s *Store) releaseChunks(ctx context.Context, sc *sql.Conn, id, fromSeq int64, ranged bool) error {
	where, args := `obj = ?`, []any{id}
	if ranged {
		where, args = `obj = ? AND seq >= ?`, []any{id, fromSeq}
	}
	grouped := `SELECT block, count(*) AS cnt FROM ` + s.chunks + ` WHERE ` + where + ` GROUP BY block`
	sub := `SELECT block FROM ` + s.chunks + ` WHERE ` + where
	for _, q := range []string{
		`UPDATE ` + s.blocks + ` SET refs = refs - m.cnt FROM (` + grouped + `) m WHERE ` + s.blocks + `.id = m.block`,
		`DELETE FROM ` + s.blocks + ` WHERE refs <= 0 AND id IN (` + sub + `)`,
		`DELETE FROM ` + s.chunks + ` WHERE ` + where,
	} {
		if _, err := sc.ExecContext(ctx, q, args...); err != nil {
			return err
		}
	}
	return nil
}

// blockOf returns the id of the block chunk (id, seq) maps to, and whether the
// chunk exists.
func (s *Store) blockOf(ctx context.Context, sc *sql.Conn, id, seq int64) (int64, bool, error) {
	var block int64
	err := sc.QueryRowContext(ctx,
		`SELECT block FROM `+s.chunks+` WHERE obj = ? AND seq = ?`, id, seq).Scan(&block)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return block, true, nil
}

// blockRefs returns the reference count of block.
func (s *Store) blockRefs(ctx context.Context, sc *sql.Conn, block int64) (int64, error) {
	var refs int64
	err := sc.QueryRowContext(ctx,
		`SELECT refs FROM `+s.blocks+` WHERE id = ?`, block).Scan(&refs)
	return refs, err
}

// newBlock inserts a new, privately-owned block (refs == 1) and returns its id.
func (s *Store) newBlock(ctx context.Context, sc *sql.Conn, data []byte, enc int) (int64, error) {
	res, err := sc.ExecContext(ctx,
		`INSERT INTO `+s.blocks+` (data, enc) VALUES (?, ?)`, data, enc)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// copyBlock duplicates block src into a new privately-owned block (refs == 1)
// and returns its id — the copy half of copy-on-write.
func (s *Store) copyBlock(ctx context.Context, sc *sql.Conn, src int64) (int64, error) {
	res, err := sc.ExecContext(ctx,
		`INSERT INTO `+s.blocks+` (data, enc) SELECT data, enc FROM `+s.blocks+` WHERE id = ?`, src)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// mapChunk points chunk (id, seq) at block, inserting or repointing the mapping.
func (s *Store) mapChunk(ctx context.Context, sc *sql.Conn, id, seq, block int64) error {
	_, err := sc.ExecContext(ctx,
		`INSERT INTO `+s.chunks+` (obj, seq, block) VALUES (?, ?, ?) `+
			`ON CONFLICT(obj, seq) DO UPDATE SET block = excluded.block`,
		id, seq, block)
	return err
}

// decBlockRef drops one reference from block. It does not free the block at
// zero: its callers (copy-on-write) decrement a block they know is still shared
// (refs was > 1), so it never reaches zero here; bulk freeing is releaseChunks'.
func (s *Store) decBlockRef(ctx context.Context, sc *sql.Conn, block int64) error {
	_, err := sc.ExecContext(ctx,
		`UPDATE `+s.blocks+` SET refs = refs - 1 WHERE id = ?`, block)
	return err
}

// onBlockBlob opens block for incremental BLOB I/O on the same physical
// connection sc (so it joins any transaction sc already holds), invokes fn with
// the handle, and closes it. It centralizes the driver-conn type assertion and
// OpenBlob/Close lifecycle for blobWrite and blobRead.
func (s *Store) onBlockBlob(sc *sql.Conn, block int64, write bool, fn func(*sqlite.Blob) error) error {
	return sc.Raw(func(dc any) error {
		c, ok := dc.(*sqlite.Conn)
		if !ok {
			return fmt.Errorf("blobstore: unexpected driver conn %T", dc)
		}
		b, err := c.OpenBlob("main", s.blocksRaw, "data", block, write)
		if err != nil {
			return err
		}
		defer b.Close()
		return fn(b)
	})
}

// blobWrite writes src at in-block offset off into block. The block must be
// privately owned (see privateRawBlock) — this mutates it in place.
func (s *Store) blobWrite(sc *sql.Conn, block int64, src []byte, off int64) error {
	return s.onBlockBlob(sc, block, true, func(b *sqlite.Blob) error {
		_, err := b.WriteAt(src, off)
		return err
	})
}

// blobRead reads len(dst) bytes at in-block offset off from block into dst. A
// short read at the physical block end is not an error here (callers clamp to
// logical size before calling).
func (s *Store) blobRead(sc *sql.Conn, block int64, dst []byte, off int64) error {
	return s.onBlockBlob(sc, block, false, func(b *sqlite.Blob) error {
		if _, err := b.ReadAt(dst, off); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	})
}
