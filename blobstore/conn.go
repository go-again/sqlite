package blobstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
)

// ConnStore runs blobstore operations on a connection the caller already holds,
// so its writes join whatever transaction is open on that connection — a SQLite
// transaction is per-connection. It is the way to fold blobstore content writes
// into a larger application transaction: e.g. a filesystem that holds one
// long-lived writer and wants object content to commit atomically with its inode
// metadata, with no flush-around-blobstore seam and no separate writer fighting
// for the write lock.
//
// Build one with [Store.OnConn]. The caller owns the transaction lifecycle — open
// it on the connection (a raw BEGIN IMMEDIATE for a write transaction; a deferred
// BEGIN, including [database/sql.DB.BeginTx], upgrades its lock on the first write
// and can then collide with another writer), run any mix of ConnStore operations and
// your own statements on that same connection, then commit or roll back. These
// methods never open or close a transaction. They also never run the
// post-delete / post-truncate incremental vacuum that the pooled [Store.Delete] /
// [Store.Truncate] do, because incremental_vacuum cannot run inside an open
// transaction — call it yourself after you commit if you need the space returned.
//
// Open a transaction first: a ConnStore driven in autocommit (no BEGIN) commits each
// statement on its own, so a Create followed by a failing WriteAt leaves a committed
// empty object — the atomicity this type exists for requires the surrounding
// transaction.
//
// Do NOT call the pooled [Store] mutators on the same Store while a transaction is
// open on this connection. [Store.NewVersion], [Store.Prune], [Store.Clone], and the
// pooled [Store.Delete] / [Store.Truncate] each acquire a SEPARATE pooled connection
// and BEGIN IMMEDIATE on it: with SetMaxOpenConns(1) that second acquire deadlocks
// (your pinned connection has exhausted the pool), and with more connections it
// blocks on your write lock and cannot see your uncommitted writes. Do a
// transaction's blobstore work through the ConnStore, and run pooled
// versioning/vacuum after you commit. (Set MaxOpenConns above the number of
// connections you pin concurrently.)
//
// Reads ([ConnStore.ReadAt], [ConnStore.Size]) are a consistent snapshot only inside
// an open transaction: outside one each underlying statement is its own implicit
// transaction, so a concurrent committed writer can tear a multi-chunk read. The
// pooled [Store] read path wraps reads in a transaction for you; the ConnStore defers
// to the transaction you control, so open one (a read transaction suffices) when you
// need a stable view.
//
// A ConnStore is bound to one connection and is no safer for concurrent use than
// that connection: drive it sequentially.
type ConnStore struct {
	s  *Store
	sc *sql.Conn
}

// OnConn returns a [ConnStore] that runs on conn, so its writes join the
// transaction the caller has open on conn. The caller owns BEGIN/COMMIT/ROLLBACK.
func (s *Store) OnConn(conn *sql.Conn) *ConnStore { return &ConnStore{s: s, sc: conn} }

// Create makes a new empty object in the caller's transaction and returns its id.
func (c *ConnStore) Create(ctx context.Context, opts ...CreateOption) (int64, error) {
	if c.s.readOnly {
		return 0, fmt.Errorf("blobstore: create: %w", ErrReadOnly)
	}
	return c.s.createOn(ctx, c.sc, opts...)
}

// WriteAt writes p at object offset off within the caller's transaction, growing
// the object on demand (sparse holes read as zero). It does not commit — the write
// becomes durable when the caller commits, and is discarded on rollback. Returns 0
// with the error on failure (the caller's transaction should then be rolled back).
func (c *ConnStore) WriteAt(ctx context.Context, id int64, p []byte, off int64) (int, error) {
	if c.s.readOnly {
		return 0, ErrReadOnly
	}
	end, ok, err := writeArgs(p, off)
	if err != nil || !ok {
		return 0, err
	}
	if err := c.s.writeOnConn(ctx, c.sc, id, p, off, end); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Batch runs fn's writes against object id within the caller's transaction, with a
// single size update at the end. Unlike [Store.Batch] it opens no transaction of
// its own — fn's writes commit (or roll back) with the caller's. The [io.WriterAt]
// passed to fn is bound to this connection; drive it sequentially.
func (c *ConnStore) Batch(ctx context.Context, id int64, fn func(w io.WriterAt) error) error {
	if c.s.readOnly {
		return ErrReadOnly
	}
	return c.s.batchOnConn(ctx, c.sc, id, fn)
}

// WriteAtFrom copies all of r into object id starting at off within the caller's
// transaction and returns the bytes written. Like [Store.WriteAtFrom] it holds the
// (caller's) write lock for the whole copy, so pre-buffer a slow source.
func (c *ConnStore) WriteAtFrom(ctx context.Context, id, off int64, r io.Reader) (int64, error) {
	if c.s.readOnly {
		return 0, fmt.Errorf("blobstore: WriteAtFrom %d: %w", id, ErrReadOnly)
	}
	if off < 0 {
		return 0, errors.New("blobstore: WriteAtFrom: negative offset")
	}
	chunk, err := c.s.chunkSizeOf(ctx, c.sc, id)
	if err != nil {
		return 0, err
	}
	var total int64
	if err := c.Batch(ctx, id, func(w io.WriterAt) error {
		var e error
		total, e = copyInto(w, r, off, chunk)
		return e
	}); err != nil {
		return 0, err
	}
	return total, nil
}

// Truncate sets object id to exactly size bytes within the caller's transaction
// (grow is sparse, shrink zeroes the boundary-chunk tail). The pooled path's
// post-shrink incremental vacuum is skipped — see [ConnStore].
func (c *ConnStore) Truncate(ctx context.Context, id, size int64) error {
	if c.s.readOnly {
		return ErrReadOnly
	}
	if size < 0 {
		return errors.New("blobstore: truncate: negative size")
	}
	_, err := c.s.truncateOnConn(ctx, c.sc, id, size)
	return err
}

// Delete removes object id and its versions within the caller's transaction. The
// post-delete incremental vacuum is skipped — see [ConnStore].
func (c *ConnStore) Delete(ctx context.Context, id int64) error {
	if c.s.readOnly {
		return ErrReadOnly
	}
	return c.s.deleteOnConn(ctx, c.sc, id)
}

// Size reports object id's logical length as seen by the caller's transaction —
// including content it wrote earlier in the same transaction, before commit.
func (c *ConnStore) Size(ctx context.Context, id int64) (int64, error) {
	return c.s.sizeOn(ctx, c.sc, id)
}

// ReadAt reads into p from object offset off as seen by the caller's transaction,
// so it observes that transaction's own uncommitted writes. It clamps to the
// logical size ([io.EOF] past the end) and zero-fills sparse holes.
func (c *ConnStore) ReadAt(ctx context.Context, id int64, p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("blobstore: ReadAt: negative offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	return c.s.readOnConn(ctx, c.sc, id, p, off)
}
