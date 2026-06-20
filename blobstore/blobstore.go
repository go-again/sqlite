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

// errCorruptChunk reports an object row whose chunk size is non-positive.
// Create only ever writes a validated positive size, so this is reachable
// only if something outside this package wrote the row — we surface it as an
// error instead of letting the chunk arithmetic divide by zero.
func errCorruptChunk(id, chunk int64) error {
	return fmt.Errorf("blobstore: object %d: invalid chunk size %d", id, chunk)
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
	if s.chunkSize <= 0 {
		return nil, errors.New("blobstore: chunk size must be positive")
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
			`chunk INTEGER NOT NULL CHECK(chunk > 0), codec INTEGER NOT NULL DEFAULT 0)`,
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
	// Tables created by an older schema lack codec/enc; add them. No-op for
	// tables this version creates (the columns are already there).
	if err := s.ensureColumn(ctx, s.objs, "codec", "INTEGER NOT NULL DEFAULT 0"); err != nil {
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

// Create inserts a new, empty object and returns its id. The object's storage
// mode (raw or compressed) is frozen here from the Store's [WithCompression]
// setting.
func (s *Store) Create(ctx context.Context) (int64, error) {
	codec := codecRaw
	if _, ok := s.compression.azLevel(); ok {
		codec = codecAZ
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO `+s.objs+` (size, chunk, codec) VALUES (0, ?, ?)`, s.chunkSize, codec)
	if err != nil {
		return 0, fmt.Errorf("blobstore: create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("blobstore: create: %w", err)
	}
	return id, nil
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
	sc, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer sc.Close()

	if err := begin(ctx, sc); err != nil {
		return err
	}
	committed := false
	defer rollbackIf(sc, &committed)

	res, err := sc.ExecContext(ctx, `DELETE FROM `+s.objs+` WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("blobstore: delete %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("blobstore: delete %d: %w", id, ErrNotFound)
	}
	if _, err := sc.ExecContext(ctx, `DELETE FROM `+s.chunks+` WHERE obj = ?`, id); err != nil {
		return fmt.Errorf("blobstore: delete %d: %w", id, err)
	}
	if err := commit(ctx, sc); err != nil {
		return err
	}
	committed = true

	if s.vacuumOnDelete {
		_, _ = sc.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	}
	return nil
}

// Truncate sets object id to exactly size bytes. Shrinking deletes the chunks
// beyond the new end and zeroes any trailing bytes inside the boundary chunk;
// growing is sparse — the gap reads back as zeros until written.
func (s *Store) Truncate(ctx context.Context, id, size int64) error {
	if size < 0 {
		return errors.New("blobstore: truncate: negative size")
	}
	sc, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer sc.Close()

	if err := begin(ctx, sc); err != nil {
		return err
	}
	committed := false
	defer rollbackIf(sc, &committed)

	var cur, chunk, codec int64
	err = sc.QueryRowContext(ctx,
		`SELECT size, chunk, codec FROM `+s.objs+` WHERE id = ?`, id).Scan(&cur, &chunk, &codec)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("blobstore: truncate %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("blobstore: truncate %d: %w", id, err)
	}
	if chunk <= 0 {
		return errCorruptChunk(id, chunk)
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
			if err := s.zeroChunkTail(ctx, sc, id, size/chunk, rem, chunk, codec == codecAZ); err != nil {
				return fmt.Errorf("blobstore: truncate %d: %w", id, err)
			}
		}
	}

	if _, err := sc.ExecContext(ctx,
		`UPDATE `+s.objs+` SET size = ? WHERE id = ?`, size, id); err != nil {
		return fmt.Errorf("blobstore: truncate %d: %w", id, err)
	}
	if err := commit(ctx, sc); err != nil {
		return err
	}
	committed = true

	if s.vacuumOnDelete && size < cur {
		_, _ = sc.ExecContext(ctx, `PRAGMA incremental_vacuum`)
	}
	return nil
}

// zeroChunkTail zeroes bytes [from:chunk) of the chunk at (id, seq) if it
// exists. Caller holds an open transaction on sc.
func (s *Store) zeroChunkTail(ctx context.Context, sc *sql.Conn, id, seq, from, chunk int64, compressed bool) error {
	if compressed {
		plain, ok, err := s.chunkGetCompressed(ctx, sc, id, seq, chunk)
		if err != nil || !ok {
			return err
		}
		for i := from; i < chunk; i++ {
			plain[i] = 0
		}
		return s.chunkPutCompressed(ctx, sc, id, seq, plain)
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
// compressed at the Store's level. Upserts the row.
func (s *Store) chunkPutCompressed(ctx context.Context, sc *sql.Conn, id, seq int64, plain []byte) error {
	data, enc, err := encodeChunk(plain, s.compression.azLevelOrDefault())
	if err != nil {
		return err
	}
	_, err = sc.ExecContext(ctx,
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
