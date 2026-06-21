package blobstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	sqlite "gosqlite.org"
	"gosqlite.org/internal/sqlid"
)

// ErrNotFound is returned (wrapped) by operations that name an object id not
// present in the store. Test for it with [errors.Is].
var ErrNotFound = errors.New("blobstore: object not found")

// Store is a managed collection of growable byte objects backed by two
// SQLite tables. Open it once with [Open] and share it; its methods are safe
// for concurrent use. A Store does not own the [gosqlite.org.DB] — closing
// the DB is the caller's job and invalidates the Store.
type Store struct {
	db *sqlite.DB

	// objs/chunks/idx are double-quoted identifiers spliced into SQL;
	// chunksRaw is the bare chunk-table name for OpenBlob, which takes an
	// identifier argument rather than SQL text.
	objs      string
	chunks    string
	chunksRaw string
	idx       string

	chunkSize      int
	vacuumOnDelete bool
	compression    Compression
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

// Open prepares a Store backed by two tables derived from name:
// "<name>_objects" (object metadata) and "<name>_chunks" (chunk data). It
// creates them and the supporting index if they do not exist, so it is safe
// to call on every startup. name must be a valid SQL identifier
// (letters, digits, underscore; not starting with a digit).
//
// The same db (and the same name) may be reopened across process restarts to
// reattach to existing objects.
func Open(db *sqlite.DB, name string, opts ...Option) (*Store, error) {
	if db == nil {
		return nil, errors.New("blobstore: nil db")
	}
	if !sqlid.ValidIdent(name) {
		return nil, fmt.Errorf("blobstore: invalid name %q (want a SQL identifier)", name)
	}
	s := &Store{
		db:        db,
		objs:      sqlid.QuoteIdent(name + "_objects"),
		chunks:    sqlid.QuoteIdent(name + "_chunks"),
		chunksRaw: name + "_chunks",
		idx:       sqlid.QuoteIdent(name + "_chunks_obj_seq"),
		chunkSize: DefaultChunkSize,
	}
	for _, o := range opts {
		o(s)
	}
	if s.chunkSize <= 0 || s.chunkSize > maxChunkSize {
		return nil, fmt.Errorf("blobstore: chunk size must be in 1..%d bytes", maxChunkSize)
	}
	if err := s.migrate(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS ` + s.objs + ` (` +
			`id INTEGER PRIMARY KEY, size INTEGER NOT NULL DEFAULT 0, ` +
			`chunk INTEGER NOT NULL CHECK(chunk > 0), codec INTEGER NOT NULL DEFAULT 0, ` +
			`level INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS ` + s.chunks + ` (` +
			`id INTEGER PRIMARY KEY, obj INTEGER NOT NULL, seq INTEGER NOT NULL, ` +
			`data BLOB NOT NULL, enc INTEGER NOT NULL DEFAULT 0)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + s.idx + ` ON ` + s.chunks + ` (obj, seq)`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("blobstore: migrate: %w", err)
		}
	}
	// Tables created by an older schema lack codec/level/enc; add them. No-op
	// for tables this version creates (the columns are already there). A level
	// of 0 means "no per-object override — use the writing Store's level", so
	// older objects keep their existing behavior.
	if err := s.ensureColumn(ctx, s.objs, "codec", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("blobstore: migrate: %w", err)
	}
	if err := s.ensureColumn(ctx, s.objs, "level", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("blobstore: migrate: %w", err)
	}
	if err := s.ensureColumn(ctx, s.chunks, "enc", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("blobstore: migrate: %w", err)
	}
	return nil
}

// ensureColumn adds col (with declaration decl) to the quoted table if absent,
// so a database created by an older schema gains the column. Idempotent.
func (s *Store) ensureColumn(ctx context.Context, quotedTable, col, decl string) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+quotedTable+`)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == col {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE `+quotedTable+` ADD COLUMN `+col+` `+decl)
	return err
}

// Create inserts a new, empty object and returns its id. Its storage MODE (raw
// or compressed) and compression LEVEL are initialized here from the Store's
// [WithCompression] setting or a per-object [WithObjectCompression] override.
// Both stay mutable: change them later with [Store.SetCompression], which
// rewrites the object's existing chunks when the mode changes. (The chunk size,
// by contrast, is fixed at Create.)
func (s *Store) Create(ctx context.Context, opts ...CreateOption) (int64, error) {
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
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO `+s.objs+` (size, chunk, codec, level) VALUES (0, ?, ?, ?)`, s.chunkSize, codec, level)
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
	// Raw chunk: the stored value is the plaintext itself (a chunk-size blob).
	var data []byte
	if err := sc.QueryRowContext(ctx,
		`SELECT data FROM `+s.chunks+` WHERE obj = ? AND seq = ?`, id, seq).Scan(&data); err != nil {
		return nil, err
	}
	return data, nil
}

// ObjectInfo is an object's storage metadata, including its actual at-rest
// compression ratio (computed from the stored chunk sizes, not maintained).
type ObjectInfo struct {
	Size        int64       // logical (uncompressed) length in bytes
	StoredBytes int64       // total bytes the object's chunks occupy on disk
	Ratio       float64     // StoredBytes/Size (0 when Size==0); below 1 means compressed or sparse
	ChunkSize   int64       // the object's chunk size
	Compressed  bool        // whether the object is stored compressed
	Level       Compression // compression level for future writes (0 = none / Store's level)
}

// Stat returns object id's storage metadata. The compression ratio and stored
// size are computed from the chunk sizes on demand (a single aggregate over the
// object's chunks), so they are always accurate without a maintained column.
func (s *Store) Stat(ctx context.Context, id int64) (ObjectInfo, error) {
	var info ObjectInfo
	var codec, level int64
	err := s.db.QueryRowContext(ctx,
		`SELECT size, chunk, codec, level, `+
			`(SELECT coalesce(sum(length(data)), 0) FROM `+s.chunks+` WHERE obj = `+s.objs+`.id) `+
			`FROM `+s.objs+` WHERE id = ?`, id).
		Scan(&info.Size, &info.ChunkSize, &codec, &level, &info.StoredBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return ObjectInfo{}, fmt.Errorf("blobstore: Stat %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("blobstore: Stat %d: %w", id, err)
	}
	info.Compressed = codec == codecAZ
	info.Level = Compression(level)
	if info.Size > 0 {
		info.Ratio = float64(info.StoredBytes) / float64(info.Size)
	}
	return info, nil
}

// Size reports the logical length in bytes of object id.
func (s *Store) Size(ctx context.Context, id int64) (int64, error) {
	var size int64
	err := s.db.QueryRowContext(ctx,
		`SELECT size FROM `+s.objs+` WHERE id = ?`, id).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("blobstore: size %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("blobstore: size %d: %w", id, err)
	}
	return size, nil
}

// Delete removes object id and frees every chunk it owns. Deleting an
// id that does not exist returns ErrNotFound. If the Store was opened with
// [WithVacuumOnDelete] and the database is in incremental auto_vacuum mode,
// the freed pages are returned to the OS.
func (s *Store) Delete(ctx context.Context, id int64) error {
	err := s.withTx(ctx, func(sc *sql.Conn) error {
		res, err := sc.ExecContext(ctx, `DELETE FROM `+s.objs+` WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("blobstore: delete %d: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("blobstore: delete %d: %w", id, err)
		}
		if n == 0 {
			return fmt.Errorf("blobstore: delete %d: %w", id, ErrNotFound)
		}
		if _, err := sc.ExecContext(ctx, `DELETE FROM `+s.chunks+` WHERE obj = ?`, id); err != nil {
			return fmt.Errorf("blobstore: delete %d: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.maybeVacuum(ctx)
	return nil
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
	err := s.withTx(ctx, func(sc *sql.Conn) error {
		var chunk, codec, level int64
		err := sc.QueryRowContext(ctx,
			`SELECT size, chunk, codec, level FROM `+s.objs+` WHERE id = ?`, id).Scan(&cur, &chunk, &codec, &level)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("blobstore: truncate %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("blobstore: truncate %d: %w", id, err)
		}
		if err := checkChunk(id, chunk); err != nil {
			return err
		}

		if size < cur {
			// Chunks whose first byte is at or past the new size are gone.
			firstDead := (size + chunk - 1) / chunk // ceil(size/chunk)
			if _, err := sc.ExecContext(ctx,
				`DELETE FROM `+s.chunks+` WHERE obj = ? AND seq >= ?`, id, firstDead); err != nil {
				return fmt.Errorf("blobstore: truncate %d: %w", id, err)
			}
			// Zero the live tail of the boundary chunk so a later re-grow reads
			// zeros there rather than resurrecting old bytes.
			if rem := size % chunk; rem != 0 {
				if err := s.zeroChunkTail(ctx, sc, id, size/chunk, rem, chunk, codec == codecAZ, Compression(level)); err != nil {
					return fmt.Errorf("blobstore: truncate %d: %w", id, err)
				}
			}
		}

		if _, err := sc.ExecContext(ctx,
			`UPDATE `+s.objs+` SET size = ? WHERE id = ?`, size, id); err != nil {
			return fmt.Errorf("blobstore: truncate %d: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if size < cur {
		s.maybeVacuum(ctx)
	}
	return nil
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
	rowid, ok, err := s.chunkRowid(ctx, sc, id, seq)
	if err != nil || !ok {
		return err
	}
	zeros := make([]byte, chunk-from)
	return s.blobWrite(sc, rowid, zeros, from)
}

// chunkGetCompressed returns the full chunk-size plaintext of (id, seq), or
// (nil, false) if the chunk row is absent (a sparse hole the caller zero-fills).
func (s *Store) chunkGetCompressed(ctx context.Context, sc *sql.Conn, id, seq, chunk int64) ([]byte, bool, error) {
	var data []byte
	var enc int
	err := sc.QueryRowContext(ctx,
		`SELECT data, enc FROM `+s.chunks+` WHERE obj = ? AND seq = ?`, id, seq).Scan(&data, &enc)
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
// Store's. Upserts the row.
func (s *Store) chunkPutCompressed(ctx context.Context, sc *sql.Conn, id, seq int64, plain []byte, objLevel Compression) error {
	data, enc, err := encodeChunk(plain, writeLevel(objLevel, s.compression))
	if err != nil {
		return err
	}
	return s.upsertChunk(ctx, sc, id, seq, data, enc)
}

// chunkPutRaw stores plain (length == chunk) as the raw chunk (id, seq): the
// stored value is the plaintext itself — a chunk-size blob OpenBlob can address
// in place — with enc reset to verbatim. Upserts the row. Used when converting
// an object to raw mode; the normal raw write path allocates with zeroblob and
// writes in place via blobWrite instead.
func (s *Store) chunkPutRaw(ctx context.Context, sc *sql.Conn, id, seq int64, plain []byte) error {
	return s.upsertChunk(ctx, sc, id, seq, plain, encVerbatim)
}

// upsertChunk inserts the data+enc of chunk (id, seq), replacing the row if it
// already exists. It is the single owner of the chunk upsert statement shared by
// the compressed and raw put paths.
func (s *Store) upsertChunk(ctx context.Context, sc *sql.Conn, id, seq int64, data []byte, enc int) error {
	_, err := sc.ExecContext(ctx,
		`INSERT INTO `+s.chunks+` (obj, seq, data, enc) VALUES (?, ?, ?, ?) `+
			`ON CONFLICT(obj, seq) DO UPDATE SET data = excluded.data, enc = excluded.enc`,
		id, seq, data, enc)
	return err
}

// chunkRowid returns the rowid of chunk (id, seq) and whether it exists.
func (s *Store) chunkRowid(ctx context.Context, sc *sql.Conn, id, seq int64) (int64, bool, error) {
	var rowid int64
	err := sc.QueryRowContext(ctx,
		`SELECT id FROM `+s.chunks+` WHERE obj = ? AND seq = ?`, id, seq).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return rowid, true, nil
}

// onChunkBlob opens the chunk row rowid for incremental BLOB I/O on the same
// physical connection sc (so it joins any transaction sc already holds),
// invokes fn with the handle, and closes it. It centralizes the
// driver-conn type assertion and OpenBlob/Close lifecycle for blobWrite and
// blobRead.
func (s *Store) onChunkBlob(sc *sql.Conn, rowid int64, write bool, fn func(*sqlite.Blob) error) error {
	return sc.Raw(func(dc any) error {
		c, ok := dc.(*sqlite.Conn)
		if !ok {
			return fmt.Errorf("blobstore: unexpected driver conn %T", dc)
		}
		b, err := c.OpenBlob("main", s.chunksRaw, "data", rowid, write)
		if err != nil {
			return err
		}
		defer b.Close()
		return fn(b)
	})
}

// blobWrite writes src at in-chunk offset off into the chunk row rowid.
func (s *Store) blobWrite(sc *sql.Conn, rowid int64, src []byte, off int64) error {
	return s.onChunkBlob(sc, rowid, true, func(b *sqlite.Blob) error {
		_, err := b.WriteAt(src, off)
		return err
	})
}

// blobRead reads len(dst) bytes at in-chunk offset off from the chunk row
// rowid into dst. A short read at the physical chunk end is not an error
// here (callers clamp to logical size before calling).
func (s *Store) blobRead(sc *sql.Conn, rowid int64, dst []byte, off int64) error {
	return s.onChunkBlob(sc, rowid, false, func(b *sqlite.Blob) error {
		if _, err := b.ReadAt(dst, off); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	})
}
