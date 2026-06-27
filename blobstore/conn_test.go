package blobstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
)

// TestOnConnConcurrentMetadata exercises concurrent OnConn usage on independent
// connections (each in its own transaction) under -race, on the METADATA path only
// (Create / Size — no BLOB I/O, which trips the upstream checkptr analyzer that
// skipUnderRace exists for). It covers the transaction-joining + pinned-connection
// threading that the BLOB-I/O OnConn tests must skip under -race.
func TestOnConnConcurrentMetadata(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	db.SetMaxOpenConns(4)

	const workers, each = 4, 15
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range each {
				conn, err := db.Conn(ctx)
				if err != nil {
					t.Errorf("Conn: %v", err)
					return
				}
				if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
					_ = conn.Close()
					t.Errorf("BEGIN: %v", err)
					return
				}
				cs := s.OnConn(conn)
				id, err := cs.Create(ctx)
				if err != nil {
					t.Errorf("Create: %v", err)
				}
				if _, err := cs.Size(ctx, id); err != nil {
					t.Errorf("Size: %v", err)
				}
				if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
					t.Errorf("COMMIT: %v", err)
				}
				_ = conn.Close()
			}
		})
	}
	wg.Wait()

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM files_objects`).Scan(&n); err != nil || n != workers*each {
		t.Fatalf("objects = (%d, %v), want %d", n, err, workers*each)
	}
}

// TestOnConnJoinsTx: object content written through OnConn joins the caller's
// open transaction — it is invisible to other connections until the caller
// commits, then commits atomically with the caller's own metadata row; a read
// through the same handle sees the not-yet-committed content.
func TestOnConnJoinsTx(t *testing.T) {
	skipUnderRace(t)
	ctx := context.Background()
	s, db := newStore(t)
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(`CREATE TABLE inode(ino INTEGER PRIMARY KEY, blob INTEGER)`); err != nil {
		t.Fatal(err)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	cs := s.OnConn(conn)

	id, err := cs.Create(ctx)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := cs.WriteAt(ctx, id, []byte("hello world"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	// The caller's own metadata write, on the SAME connection → same transaction.
	if _, err := conn.ExecContext(ctx, `INSERT INTO inode(ino, blob) VALUES(1, ?)`, id); err != nil {
		t.Fatal(err)
	}

	// Read-after-write within the transaction sees the uncommitted content.
	buf := make([]byte, 11)
	if n, err := cs.ReadAt(ctx, id, buf, 0); (err != nil && err != io.EOF) || string(buf[:n]) != "hello world" {
		t.Fatalf("in-tx ReadAt = (%q, %v), want hello world", buf[:n], err)
	}

	// A pooled reader on another connection does NOT see the uncommitted object.
	if _, err := s.Size(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pooled Size before commit = %v, want ErrNotFound (uncommitted)", err)
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}

	// After commit both the content and the inode row are visible to the pool.
	if got := readAll(t, s, id); string(got) != "hello world" {
		t.Fatalf("after commit content = %q, want hello world", got)
	}
	var blob int64
	if err := db.QueryRow(`SELECT blob FROM inode WHERE ino = 1`).Scan(&blob); err != nil || blob != id {
		t.Fatalf("inode row after commit = (%d, %v), want %d", blob, err, id)
	}
}

// TestOnConnRollback: content written through OnConn rolls back with the caller's
// transaction — nothing persists.
func TestOnConnRollback(t *testing.T) {
	skipUnderRace(t)
	ctx := context.Background()
	s, db := newStore(t)
	db.SetMaxOpenConns(4)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	cs := s.OnConn(conn)
	id, err := cs.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.WriteAt(ctx, id, []byte("discard me"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Size(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Size after rollback = %v, want ErrNotFound (nothing persisted)", err)
	}
}

// TestOnConnReadOnly: every ConnStore mutator refuses on a read-only store with
// ErrReadOnly (the guard returns before any SQL, so this runs under -race too),
// while reads through the same handle still work. The explicit per-method guards
// matter because ConnStore bypasses the pooled path's withTx (which is where the
// pooled Store enforces read-only), so a dropped guard would silently allow writes.
func TestOnConnReadOnly(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	id, err := s.Create(ctx) // seed one object through the writable handle
	if err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(db, "files")
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cs := ro.OnConn(conn)

	if _, err := cs.Create(ctx); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Create = %v, want ErrReadOnly", err)
	}
	if _, err := cs.WriteAt(ctx, id, []byte("x"), 0); !errors.Is(err, ErrReadOnly) {
		t.Errorf("WriteAt = %v, want ErrReadOnly", err)
	}
	if err := cs.Batch(ctx, id, func(io.WriterAt) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Batch = %v, want ErrReadOnly", err)
	}
	if _, err := cs.WriteAtFrom(ctx, id, 0, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrReadOnly) {
		t.Errorf("WriteAtFrom = %v, want ErrReadOnly", err)
	}
	if err := cs.Truncate(ctx, id, 0); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Truncate = %v, want ErrReadOnly", err)
	}
	if err := cs.Delete(ctx, id); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Delete = %v, want ErrReadOnly", err)
	}
	// Reads still work on the read-only handle.
	if _, err := cs.Size(ctx, id); err != nil {
		t.Errorf("Size on read-only ConnStore = %v, want nil", err)
	}
}

// TestOnConnTruncateDeleteRollback: a Truncate and a Delete issued through OnConn
// inside a caller transaction both roll back with it — the committed object is
// unchanged. (TestOnConnRollback covers WriteAt; this covers the other two
// destructive mutators, whose post-op incremental_vacuum is the caller's to run.)
func TestOnConnTruncateDeleteRollback(t *testing.T) {
	skipUnderRace(t)
	ctx := context.Background()
	s, db := newStore(t)
	db.SetMaxOpenConns(4)

	id, err := s.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("hello world") // 11 bytes, committed through the pooled handle
	if _, err := s.WriteAtFrom(ctx, id, 0, bytes.NewReader(want)); err != nil {
		t.Fatal(err)
	}

	rollback := func(mutate func(cs *ConnStore) error) {
		t.Helper()
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
			t.Fatal(err)
		}
		if err := mutate(s.OnConn(conn)); err != nil {
			t.Fatalf("in-tx mutate: %v", err)
		}
		if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
			t.Fatal(err)
		}
	}

	rollback(func(cs *ConnStore) error { return cs.Truncate(ctx, id, 4) })
	if got := readAll(t, s, id); !bytes.Equal(got, want) {
		t.Fatalf("after Truncate rollback content = %q, want %q", got, want)
	}
	rollback(func(cs *ConnStore) error { return cs.Delete(ctx, id) })
	if got := readAll(t, s, id); !bytes.Equal(got, want) {
		t.Fatalf("after Delete rollback content = %q, want %q (object should still exist)", got, want)
	}
}

// TestOnConnMultiChunkOneTx: multi-chunk content streams into one object inside a
// single caller transaction (the seam OnConn removes — pooled blobstore would open
// its own transaction per write).
func TestOnConnMultiChunkOneTx(t *testing.T) {
	skipUnderRace(t)
	ctx := context.Background()
	s, db := newStore(t, WithChunkSize(1024)) // small chunks so a few KB spans several
	db.SetMaxOpenConns(4)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	cs := s.OnConn(conn)

	id, err := cs.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("ABCDEFGH"), 1024) // 8 KiB → 8 chunks
	if n, err := cs.WriteAtFrom(ctx, id, 0, bytes.NewReader(want)); err != nil || n != int64(len(want)) {
		t.Fatalf("WriteAtFrom = (%d, %v), want %d", n, err, len(want))
	}
	if sz, err := cs.Size(ctx, id); err != nil || sz != int64(len(want)) {
		t.Fatalf("in-tx Size = (%d, %v), want %d", sz, err, len(want))
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}

	if got := readAll(t, s, id); !bytes.Equal(got, want) {
		t.Fatalf("multi-chunk content mismatch after commit (%d vs %d bytes)", len(got), len(want))
	}
}
