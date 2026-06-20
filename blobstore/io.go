package blobstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
)

// Writer is an [io.WriterAt] + [io.Closer] over one object. WriteAt grows the
// object on demand; offsets may be written in any order and gaps are sparse.
// Close releases the handle (no buffered state — each WriteAt is durable on
// return). A Writer is safe for concurrent WriteAt calls; see the package doc
// on serialization. The handle uses the context passed to [Store.Writer].
type Writer struct {
	s   *Store
	ctx context.Context
	id  int64
}

// Writer returns a [Writer] for object id, after confirming it exists.
func (s *Store) Writer(ctx context.Context, id int64) (*Writer, error) {
	if _, err := s.Size(ctx, id); err != nil {
		return nil, err
	}
	return &Writer{s: s, ctx: ctx, id: id}, nil
}

// WriteAt implements [io.WriterAt].
func (w *Writer) WriteAt(p []byte, off int64) (int, error) {
	return w.s.writeAt(w.ctx, w.id, p, off)
}

// Close is a no-op that satisfies io.Closer; a Writer holds no connection or
// buffered state of its own — each WriteAt acquires and releases its
// resources — so there is nothing to release.
func (w *Writer) Close() error { return nil }

// Reader is an [io.ReaderAt] + [io.Closer] over one object. ReadAt clamps to
// the object's logical size (returning [io.EOF] past the end) and zero-fills
// sparse holes. The handle uses the context passed to [Store.Reader].
type Reader struct {
	s   *Store
	ctx context.Context
	id  int64
}

// Reader returns a [Reader] for object id, after confirming it exists.
func (s *Store) Reader(ctx context.Context, id int64) (*Reader, error) {
	if _, err := s.Size(ctx, id); err != nil {
		return nil, err
	}
	return &Reader{s: s, ctx: ctx, id: id}, nil
}

// ReadAt implements [io.ReaderAt].
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	return r.s.readAt(r.ctx, r.id, p, off)
}

// Close is a no-op that satisfies io.Closer; a Reader holds no connection or
// buffered state of its own — each ReadAt acquires and releases its
// resources — so there is nothing to release.
func (r *Reader) Close() error { return nil }

// writeAt writes p at object offset off, allocating chunks as needed, in a
// single BEGIN IMMEDIATE transaction so the whole call is atomic. It dispatches
// each chunk span to the raw (in-place) or compressed (read-modify-write) path
// per the object's stored codec.
func (s *Store) writeAt(ctx context.Context, id int64, p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("blobstore: WriteAt: negative offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	sc, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer sc.Close()

	var chunk, codec int64
	err = sc.QueryRowContext(ctx,
		`SELECT chunk, codec FROM `+s.objs+` WHERE id = ?`, id).Scan(&chunk, &codec)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("blobstore: WriteAt %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("blobstore: WriteAt %d: %w", id, err)
	}
	if chunk <= 0 {
		return 0, errCorruptChunk(id, chunk)
	}
	end := off + int64(len(p))
	if end < off { // off + len overflowed int64
		return 0, errors.New("blobstore: WriteAt: offset + length overflows int64")
	}

	if err := begin(ctx, sc); err != nil {
		return 0, err
	}
	committed := false
	defer rollbackIf(sc, &committed)

	compressed := codec == codecAZ
	written := 0
	err = eachChunkSpan(off, int64(len(p)), chunk, func(seq, inOff, span, bufOff int64) error {
		src := p[bufOff : bufOff+span]
		var werr error
		if compressed {
			werr = s.writeChunkCompressed(ctx, sc, id, seq, chunk, inOff, src)
		} else {
			werr = s.writeChunkRaw(ctx, sc, id, seq, chunk, inOff, src)
		}
		if werr != nil {
			return werr
		}
		written += int(span)
		return nil
	})
	if err != nil {
		return written, fmt.Errorf("blobstore: WriteAt %d: %w", id, err)
	}

	if _, err := sc.ExecContext(ctx,
		`UPDATE `+s.objs+` SET size = max(size, ?) WHERE id = ?`, end, id); err != nil {
		return written, fmt.Errorf("blobstore: WriteAt %d: %w", id, err)
	}
	if err := commit(ctx, sc); err != nil {
		return written, err
	}
	committed = true
	return written, nil
}

// readAt reads into p from object offset off, clamping to logical size and
// zero-filling sparse holes. It runs inside a read-only snapshot transaction
// — opened before the size/chunk lookup — so the clamping size and every
// chunk read come from one consistent view, even under a concurrent writer.
func (s *Store) readAt(ctx context.Context, id int64, p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("blobstore: ReadAt: negative offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	sc, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer sc.Close()

	// Open the snapshot first, then read size/chunk inside it. Release with
	// ROLLBACK on the way out — a read transaction has nothing to commit, and
	// the background ctx keeps a cancelled ctx from stranding it on the conn.
	if _, err := sc.ExecContext(ctx, "BEGIN"); err != nil {
		return 0, fmt.Errorf("blobstore: ReadAt %d: %w", id, err)
	}
	defer func() { _, _ = sc.ExecContext(context.Background(), "ROLLBACK") }()

	var size, chunk, codec int64
	err = sc.QueryRowContext(ctx,
		`SELECT size, chunk, codec FROM `+s.objs+` WHERE id = ?`, id).Scan(&size, &chunk, &codec)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("blobstore: ReadAt %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("blobstore: ReadAt %d: %w", id, err)
	}
	if chunk <= 0 {
		return 0, errCorruptChunk(id, chunk)
	}
	if off >= size {
		return 0, io.EOF
	}
	n := min(int64(len(p)), size-off)

	compressed := codec == codecAZ
	read := 0
	err = eachChunkSpan(off, n, chunk, func(seq, inOff, span, bufOff int64) error {
		dst := p[bufOff : bufOff+span]
		var rerr error
		if compressed {
			rerr = s.readChunkCompressed(ctx, sc, id, seq, chunk, inOff, dst)
		} else {
			rerr = s.readChunkRaw(ctx, sc, id, seq, inOff, dst)
		}
		if rerr != nil {
			return rerr
		}
		read += int(span)
		return nil
	})
	if err != nil {
		return read, fmt.Errorf("blobstore: ReadAt %d: %w", id, err)
	}
	if off+n >= size {
		return read, io.EOF
	}
	return read, nil
}

// eachChunkSpan iterates the chunk-aligned spans covering [off, off+n),
// calling fn(seq, inOff, span, bufOff) for each: seq is the chunk index, inOff
// the offset within that chunk, span the byte count, and bufOff the offset into
// the caller's buffer. Requires chunk > 0.
func eachChunkSpan(off, n, chunk int64, fn func(seq, inOff, span, bufOff int64) error) error {
	end := off + n
	var bufOff int64
	for pos := off; pos < end; {
		seq := pos / chunk
		inOff := pos % chunk
		span := min(chunk-inOff, end-pos)
		if err := fn(seq, inOff, span, bufOff); err != nil {
			return err
		}
		bufOff += span
		pos += span
	}
	return nil
}

// writeChunkRaw writes src at in-chunk offset inOff into chunk (id, seq) via
// in-place incremental BLOB I/O, allocating the chunk (zeroblob) on first use.
func (s *Store) writeChunkRaw(ctx context.Context, sc *sql.Conn, id, seq, chunk, inOff int64, src []byte) error {
	rowid, ok, err := s.chunkRowid(ctx, sc, id, seq)
	if err != nil {
		return err
	}
	if !ok {
		res, err := sc.ExecContext(ctx,
			`INSERT INTO `+s.chunks+` (obj, seq, data) VALUES (?, ?, zeroblob(?))`, id, seq, chunk)
		if err != nil {
			return err
		}
		if rowid, err = res.LastInsertId(); err != nil {
			return err
		}
	}
	return s.blobWrite(sc, rowid, src, inOff)
}

// writeChunkCompressed writes src at in-chunk offset inOff into chunk (id, seq)
// of a compressed object. A write covering the whole chunk skips the read (fast
// path); a partial write read-modify-writes the chunk's plaintext.
func (s *Store) writeChunkCompressed(ctx context.Context, sc *sql.Conn, id, seq, chunk, inOff int64, src []byte) error {
	if inOff == 0 && int64(len(src)) == chunk { // full chunk — no read needed
		return s.chunkPutCompressed(ctx, sc, id, seq, src)
	}
	plain, ok, err := s.chunkGetCompressed(ctx, sc, id, seq, chunk)
	if err != nil {
		return err
	}
	if !ok {
		plain = make([]byte, chunk) // sparse: start from zeros
	}
	copy(plain[inOff:], src)
	return s.chunkPutCompressed(ctx, sc, id, seq, plain)
}

// readChunkRaw reads into dst at in-chunk offset inOff from chunk (id, seq); a
// missing chunk is a sparse hole and dst is zero-filled.
func (s *Store) readChunkRaw(ctx context.Context, sc *sql.Conn, id, seq, inOff int64, dst []byte) error {
	rowid, ok, err := s.chunkRowid(ctx, sc, id, seq)
	if err != nil {
		return err
	}
	if !ok {
		for i := range dst {
			dst[i] = 0
		}
		return nil
	}
	return s.blobRead(sc, rowid, dst, inOff)
}

// readChunkCompressed fills dst from the decompressed plaintext of chunk
// (id, seq) at in-chunk offset inOff; a missing chunk zero-fills dst.
func (s *Store) readChunkCompressed(ctx context.Context, sc *sql.Conn, id, seq, chunk, inOff int64, dst []byte) error {
	plain, ok, err := s.chunkGetCompressed(ctx, sc, id, seq, chunk)
	if err != nil {
		return err
	}
	if !ok {
		for i := range dst {
			dst[i] = 0
		}
		return nil
	}
	copy(dst, plain[inOff:inOff+int64(len(dst))])
	return nil
}

// --- small SQL helpers -----------------------------------------------------

// begin opens a write transaction that takes the write lock immediately, so
// the read-modify-write of an object's size is race-free.
func begin(ctx context.Context, sc *sql.Conn) error {
	if _, err := sc.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("blobstore: begin: %w", err)
	}
	return nil
}

func commit(ctx context.Context, sc *sql.Conn) error {
	if _, err := sc.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("blobstore: commit: %w", err)
	}
	return nil
}

// rollbackIf rolls back when committed is still false (error or panic path).
// It runs on a fresh background context so a cancelled ctx can't strand an
// open transaction on the pooled connection.
func rollbackIf(sc *sql.Conn, committed *bool) {
	if !*committed {
		_, _ = sc.ExecContext(context.Background(), "ROLLBACK")
	}
}
