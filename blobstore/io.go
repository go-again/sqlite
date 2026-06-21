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

// writeArgs validates a WriteAt's offset and length and returns the end offset
// off+len(p). ok is false for a zero-length write (nothing to do, no error).
func writeArgs(p []byte, off int64) (end int64, ok bool, err error) {
	if off < 0 {
		return 0, false, errors.New("blobstore: WriteAt: negative offset")
	}
	if len(p) == 0 {
		return 0, false, nil
	}
	end = off + int64(len(p))
	if end < off { // off + len overflowed int64
		return 0, false, errors.New("blobstore: WriteAt: offset + length overflows int64")
	}
	return end, true, nil
}

// objWriteMeta reads the object row a write needs (chunk size, mode, level)
// INSIDE an open transaction, mapping a missing object to ErrNotFound and
// validating the chunk size. Reading the row under the write lock means a
// concurrent SetCompression mode-convert is serialized against the write, so the
// raw-vs-compressed dispatch can't act on a stale mode. op names the operation
// for error context.
func (s *Store) objWriteMeta(ctx context.Context, sc *sql.Conn, id int64, op string) (chunk, codec, level int64, err error) {
	err = sc.QueryRowContext(ctx,
		`SELECT chunk, codec, level FROM `+s.objs+` WHERE id = ?`, id).Scan(&chunk, &codec, &level)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, fmt.Errorf("blobstore: %s %d: %w", op, id, ErrNotFound)
	}
	if err != nil {
		return 0, 0, 0, fmt.Errorf("blobstore: %s %d: %w", op, id, err)
	}
	if err = checkChunk(id, chunk); err != nil {
		return 0, 0, 0, err
	}
	return chunk, codec, level, nil
}

// writeSpans writes p at object offset off on the in-transaction conn sc,
// dispatching each chunk span to the raw (in-place) or compressed
// (read-modify-write) path per the object's already-read codec/level. It manages
// no transaction and updates no size — the caller owns those.
func (s *Store) writeSpans(ctx context.Context, sc *sql.Conn, id, chunk, codec, level, off int64, p []byte) error {
	compressed := codec == codecAZ
	objLevel := Compression(level)
	return eachChunkSpan(off, int64(len(p)), chunk, func(seq, inOff, span, bufOff int64) error {
		src := p[bufOff : bufOff+span]
		if compressed {
			return s.writeChunkCompressed(ctx, sc, id, seq, chunk, inOff, src, objLevel)
		}
		return s.writeChunkRaw(ctx, sc, id, seq, chunk, inOff, src)
	})
}

// writeAt writes p at object offset off, allocating chunks as needed, in a
// single BEGIN IMMEDIATE transaction so the whole call is atomic.
func (s *Store) writeAt(ctx context.Context, id int64, p []byte, off int64) (int, error) {
	end, ok, err := writeArgs(p, off)
	if err != nil || !ok {
		return 0, err
	}
	err = s.withTx(ctx, func(sc *sql.Conn) error {
		chunk, codec, level, err := s.objWriteMeta(ctx, sc, id, "WriteAt")
		if err != nil {
			return err
		}
		if err := s.writeSpans(ctx, sc, id, chunk, codec, level, off, p); err != nil {
			return fmt.Errorf("blobstore: WriteAt %d: %w", id, err)
		}
		if _, err := sc.ExecContext(ctx,
			`UPDATE `+s.objs+` SET size = max(size, ?) WHERE id = ?`, end, id); err != nil {
			return fmt.Errorf("blobstore: WriteAt %d: %w", id, err)
		}
		return nil
	})
	if err != nil {
		// The transaction rolled back (or commit failed): nothing was persisted,
		// so report 0 written per io.WriterAt rather than a count that didn't stick.
		return 0, err
	}
	return len(p), nil
}

// Batch runs fn's writes against object id in a single transaction: all of fn's
// WriteAt calls commit together when fn returns nil, and roll back if fn returns
// an error or panics. It amortizes the per-write transaction (one fsync, one
// size update, one lock acquisition for the whole batch) and makes the batch
// atomic — useful for bulk-loading or streaming an object.
//
// The [io.WriterAt] passed to fn is valid only for the duration of the call and
// is NOT safe for concurrent use (it is bound to one connection/transaction);
// drive its WriteAt calls sequentially. [Store.WriteAt] is unaffected — it keeps
// its own per-call transaction and stays safe for concurrent use across handles.
//
// Batch holds the write lock for the whole callback, so keep fn tight: do not
// block on slow I/O (a network read) while inside it — buffer the source first,
// or split a large object across several Batch calls. Returns [ErrNotFound] if
// id does not exist.
func (s *Store) Batch(ctx context.Context, id int64, fn func(w io.WriterAt) error) error {
	return s.withTx(ctx, func(sc *sql.Conn) error {
		chunk, codec, level, err := s.objWriteMeta(ctx, sc, id, "Batch")
		if err != nil {
			return err
		}
		bw := &batchWriter{s: s, ctx: ctx, sc: sc, id: id, chunk: chunk, codec: codec, level: level}
		if err := fn(bw); err != nil {
			return err
		}
		if bw.maxEnd > 0 {
			if _, err := sc.ExecContext(ctx,
				`UPDATE `+s.objs+` SET size = max(size, ?) WHERE id = ?`, bw.maxEnd, id); err != nil {
				return fmt.Errorf("blobstore: Batch %d: %w", id, err)
			}
		}
		return nil
	})
}

// batchWriter is the io.WriterAt handed to a [Store.Batch] callback. It writes on
// the batch's single pinned connection/transaction — no per-call transaction or
// size update — and tracks the high-water end offset so Batch can do one size
// update at commit.
type batchWriter struct {
	s                   *Store
	ctx                 context.Context
	sc                  *sql.Conn
	id                  int64
	chunk, codec, level int64
	maxEnd              int64
}

// WriteAt implements [io.WriterAt] within a batch. A write error leaves the
// batch's transaction to be rolled back by Batch, so it reports 0 written.
func (b *batchWriter) WriteAt(p []byte, off int64) (int, error) {
	end, ok, err := writeArgs(p, off)
	if err != nil || !ok {
		return 0, err
	}
	if err := b.s.writeSpans(b.ctx, b.sc, b.id, b.chunk, b.codec, b.level, off, p); err != nil {
		return 0, fmt.Errorf("blobstore: Batch %d WriteAt: %w", b.id, err)
	}
	if end > b.maxEnd {
		b.maxEnd = end
	}
	return len(p), nil
}

// WriteAtFrom copies all of r into object id starting at off, in one [Store.Batch]
// (a single transaction), and returns the number of bytes written. Because it
// holds the write lock for the whole copy, r should be a fast or local (or
// pre-buffered) reader; for a slow source, read it into memory first or drive
// Batch yourself in segments. Like Batch it is atomic: on error nothing is
// persisted and it returns 0.
func (s *Store) WriteAtFrom(ctx context.Context, id, off int64, r io.Reader) (int64, error) {
	if off < 0 {
		return 0, errors.New("blobstore: WriteAtFrom: negative offset")
	}
	var total int64
	err := s.Batch(ctx, id, func(w io.WriterAt) error {
		buf := make([]byte, s.chunkSize) // staging buffer for reads
		pos := off
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				if _, werr := w.WriteAt(buf[:n], pos); werr != nil {
					return werr
				}
				pos += int64(n)
				total += int64(n)
			}
			if rerr == io.EOF {
				return nil
			}
			if rerr != nil {
				return rerr
			}
		}
	})
	if err != nil {
		return 0, err
	}
	return total, nil
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
	if err := checkChunk(id, chunk); err != nil {
		return 0, err
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
func (s *Store) writeChunkCompressed(ctx context.Context, sc *sql.Conn, id, seq, chunk, inOff int64, src []byte, objLevel Compression) error {
	if inOff == 0 && int64(len(src)) == chunk { // full chunk — no read needed
		return s.chunkPutCompressed(ctx, sc, id, seq, src, objLevel)
	}
	plain, ok, err := s.chunkGetCompressed(ctx, sc, id, seq, chunk)
	if err != nil {
		return err
	}
	if !ok {
		plain = make([]byte, chunk) // sparse: start from zeros
	}
	copy(plain[inOff:], src)
	return s.chunkPutCompressed(ctx, sc, id, seq, plain, objLevel)
}

// readChunkRaw reads into dst at in-chunk offset inOff from chunk (id, seq); a
// missing chunk is a sparse hole and dst is zero-filled.
func (s *Store) readChunkRaw(ctx context.Context, sc *sql.Conn, id, seq, inOff int64, dst []byte) error {
	rowid, ok, err := s.chunkRowid(ctx, sc, id, seq)
	if err != nil {
		return err
	}
	if !ok {
		clear(dst)
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
		clear(dst)
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

// withTx borrows a pooled connection, runs fn inside a single BEGIN IMMEDIATE
// write transaction, and commits — rolling back if fn returns an error or
// panics. It is the shared write-path lifecycle for the mutating methods; the
// transaction is all-or-nothing, so a caller that gets a non-nil error knows
// nothing was persisted. (readAt uses a read-only snapshot with nothing to
// commit and stays separate.)
func (s *Store) withTx(ctx context.Context, fn func(sc *sql.Conn) error) error {
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
	if err := fn(sc); err != nil {
		return err
	}
	if err := commit(ctx, sc); err != nil {
		return err
	}
	committed = true
	return nil
}
