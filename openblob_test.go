package sqlite

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"testing"
)

// openBlobConn opens an in-memory DB pinned to one conn, creates a table
// holding a zeroblob, and returns the underlying *Conn ready for OpenBlob.
func openBlobConn(t *testing.T, size int) (db *sql.DB, conn *sql.Conn, c *Conn, rowid int64) {
	t.Helper()
	if raceEnabled {
		t.Skip("skipping under -race: modernc Xsqlite3_blob_open trips Go's checkptr analyzer (upstream issue)")
	}
	d, err := sql.Open(DriverNameSQLite3, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.SetMaxOpenConns(1)

	if _, err := d.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, b BLOB)`); err != nil {
		t.Fatal(err)
	}
	res, err := d.Exec(`INSERT INTO t(b) VALUES (zeroblob(?))`, size)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	sc, err := d.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close() })

	if err := sc.Raw(func(dc any) error {
		c = dc.(*Conn)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return d, sc, c, id
}

func TestOpenBlob_WriteThenRead(t *testing.T) {
	const size = 128
	_, _, c, rowid := openBlobConn(t, size)

	bw, err := c.OpenBlob("main", "t", "b", rowid, true)
	if err != nil {
		t.Fatalf("OpenBlob write: %v", err)
	}
	if got := bw.Size(); got != size {
		t.Errorf("Size = %d, want %d", got, size)
	}

	want := make([]byte, size)
	for i := range want {
		want[i] = byte(i)
	}
	n, err := bw.WriteAt(want, 0)
	if err != nil || n != size {
		t.Fatalf("WriteAt: n=%d err=%v", n, err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}

	br, err := c.OpenBlob("main", "t", "b", rowid, false)
	if err != nil {
		t.Fatalf("OpenBlob read: %v", err)
	}
	defer br.Close()

	got := make([]byte, size)
	n, err = br.ReadAt(got, 0)
	// Reading exactly to the end returns io.EOF along with all the bytes.
	if n != size {
		t.Errorf("ReadAt n=%d, want %d", n, size)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		t.Errorf("ReadAt err=%v, want nil or EOF", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("readback mismatch")
	}
}

func TestOpenBlob_WriteOnReadOnlyRejected(t *testing.T) {
	_, _, c, rowid := openBlobConn(t, 16)
	b, err := c.OpenBlob("main", "t", "b", rowid, false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.WriteAt([]byte("x"), 0); err == nil {
		t.Error("WriteAt on read-only handle: want error, got nil")
	}
}

func TestOpenBlob_OutOfRangeWrite(t *testing.T) {
	_, _, c, rowid := openBlobConn(t, 4)
	b, err := c.OpenBlob("main", "t", "b", rowid, true)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.WriteAt([]byte("toolong"), 0); err == nil {
		t.Error("WriteAt past end: want error, got nil")
	}
	if _, err := b.WriteAt([]byte("ab"), 3); err == nil {
		t.Error("WriteAt straddling end: want error, got nil")
	}
}

func TestOpenBlob_ReadPastEnd(t *testing.T) {
	const size = 8
	_, _, c, rowid := openBlobConn(t, size)
	b, err := c.OpenBlob("main", "t", "b", rowid, false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	buf := make([]byte, 16)
	n, err := b.ReadAt(buf, 0)
	if n != size {
		t.Errorf("ReadAt n=%d, want %d", n, size)
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("ReadAt err=%v, want io.EOF", err)
	}
	n, err = b.ReadAt(buf, size+1)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("read past end: n=%d err=%v", n, err)
	}
}

func TestOpenBlob_Reopen(t *testing.T) {
	_, sc, c, _ := openBlobConn(t, 4)
	// MaxOpenConns is pinned to 1 by openBlobConn; route the second INSERT
	// through the pinned *sql.Conn so it doesn't deadlock waiting on
	// itself.
	res, err := sc.ExecContext(t.Context(), `INSERT INTO t(b) VALUES (zeroblob(8))`)
	if err != nil {
		t.Fatal(err)
	}
	second, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	b, err := c.OpenBlob("main", "t", "b", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if _, err := b.WriteAt([]byte("abcd"), 0); err != nil {
		t.Fatal(err)
	}
	if err := b.Reopen(second); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if got := b.Size(); got != 8 {
		t.Errorf("after Reopen Size=%d, want 8", got)
	}
	if _, err := b.WriteAt([]byte("01234567"), 0); err != nil {
		t.Fatalf("WriteAt after Reopen: %v", err)
	}
}

func TestOpenBlob_CloseIsIdempotent(t *testing.T) {
	_, _, c, rowid := openBlobConn(t, 4)
	b, err := c.OpenBlob("main", "t", "b", rowid, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if _, err := b.ReadAt(make([]byte, 1), 0); err == nil {
		t.Error("ReadAt after Close: want error, got nil")
	}
}

func TestOpenBlob_ReadWriteSeekRoundTrip(t *testing.T) {
	const size = 32
	_, _, c, rowid := openBlobConn(t, size)
	b, err := c.OpenBlob("main", "t", "b", rowid, true)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	want := bytes.Repeat([]byte{0xAB}, size)
	n, err := b.Write(want)
	if err != nil || n != size {
		t.Fatalf("Write: n=%d err=%v", n, err)
	}
	if _, err := b.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(b)
	// io.ReadAll consumes until EOF.
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("roundtrip mismatch")
	}
}

func TestOpenBlob_MissingRowRejected(t *testing.T) {
	_, _, c, _ := openBlobConn(t, 4)
	if _, err := c.OpenBlob("main", "t", "b", 9999, false); err == nil {
		t.Error("OpenBlob on missing rowid: want error, got nil")
	}
}
