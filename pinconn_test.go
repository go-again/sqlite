package sqlite

import (
	"context"
	"testing"
)

func TestPinConn(t *testing.T) {
	// Shared in-memory so the pinned conn and the pool see the same DB.
	db, err := OpenShared("pinconn_" + t.Name())
	if err != nil {
		t.Fatalf("OpenShared: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE t(b BLOB)`); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	res, err := db.ExecContext(ctx, `INSERT INTO t(b) VALUES (zeroblob(4))`)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	rowid, _ := res.LastInsertId()

	c, release, err := db.PinConn(ctx)
	if err != nil {
		t.Fatalf("PinConn: %v", err)
	}
	if c == nil {
		t.Fatal("PinConn returned a nil *Conn")
	}

	// The whole point: a connection-scoped op (OpenBlob) without the
	// db.Conn + Raw + type-assert dance.
	b, err := c.OpenBlob("main", "t", "b", rowid, true)
	if err != nil {
		t.Fatalf("OpenBlob: %v", err)
	}
	if _, err := b.WriteAt([]byte("data"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("blob Close: %v", err)
	}

	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// Connection returned to the pool: pinning again still succeeds.
	c2, release2, err := db.PinConn(ctx)
	if err != nil || c2 == nil {
		t.Fatalf("second PinConn = (%v, %v)", c2, err)
	}
	if err := release2(); err != nil {
		t.Fatalf("second release: %v", err)
	}

	var got []byte
	if err := db.QueryRowContext(ctx, `SELECT b FROM t`).Scan(&got); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if string(got) != "data" {
		t.Fatalf("blob contents = %q, want %q", got, "data")
	}
}
