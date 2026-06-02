package blobio_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/blobio"
)

// openDB pins a single conn, registers blobio on it, and creates a tiny
// blobs table the tests can write into.
func openDB(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	if raceEnabled {
		t.Skip("skipping under -race: modernc Xsqlite3_blob_open trips Go's checkptr analyzer (upstream issue)")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return blobio.Register(c)
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := sc.ExecContext(context.Background(),
		`CREATE TABLE blobs (id INTEGER PRIMARY KEY, b BLOB)`); err != nil {
		t.Fatal(err)
	}
	return db, sc
}

func TestBlobio_WriteThenRead(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	res, err := sc.ExecContext(ctx, `INSERT INTO blobs(b) VALUES (zeroblob(?))`, 16)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("hello, blob!!!!\x00")
	var written int64
	if err := sc.QueryRowContext(ctx,
		`SELECT writeblob('main', 'blobs', 'b', ?, 0, ?)`, id, payload).Scan(&written); err != nil {
		t.Fatalf("writeblob: %v", err)
	}
	if int(written) != len(payload) {
		t.Errorf("writeblob returned %d, want %d", written, len(payload))
	}

	var got []byte
	if err := sc.QueryRowContext(ctx,
		`SELECT readblob('main', 'blobs', 'b', ?, 0, ?)`, id, len(payload)).Scan(&got); err != nil {
		t.Fatalf("readblob: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("roundtrip mismatch: got %q, want %q", got, payload)
	}
}

func TestBlobio_PartialReadAtOffset(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	_, err := sc.ExecContext(ctx,
		`INSERT INTO blobs(id, b) VALUES (1, x'00112233445566778899AABBCCDDEEFF')`)
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err := sc.QueryRowContext(ctx,
		`SELECT readblob('main', 'blobs', 'b', 1, 4, 4)`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x44, 0x55, 0x66, 0x77}
	if !bytes.Equal(got, want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestBlobio_WritePastEndRejected(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	res, err := sc.ExecContext(ctx, `INSERT INTO blobs(b) VALUES (zeroblob(4))`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	_, err = sc.ExecContext(ctx,
		`SELECT writeblob('main', 'blobs', 'b', ?, 0, ?)`, id, []byte("toolong"))
	if err == nil {
		t.Error("writeblob past end: want error, got nil")
	}
}

func TestBlobio_MissingRowError(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	_, err := sc.ExecContext(ctx,
		`SELECT readblob('main', 'blobs', 'b', 9999, 0, 4)`)
	if err == nil {
		t.Error("readblob on missing rowid: want error, got nil")
	}
}

func TestBlobio_TextWriteAccepted(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	res, err := sc.ExecContext(ctx, `INSERT INTO blobs(b) VALUES (zeroblob(5))`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	// writeblob accepts TEXT and converts to the underlying byte sequence.
	if _, err := sc.ExecContext(ctx,
		`SELECT writeblob('main', 'blobs', 'b', ?, 0, 'hello')`, id); err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err := sc.QueryRowContext(ctx, `SELECT b FROM blobs WHERE id=?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Errorf("got %q, want \"hello\"", got)
	}
}
