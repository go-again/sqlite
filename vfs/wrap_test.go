package vfs_test

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "gosqlite.org"
	"gosqlite.org/vfs"
)

// countRec is a Recorder that tallies observed operations.
type countRec struct {
	mu                          sync.Mutex
	opens, reads, writes, syncs int
	readBytes, writeBytes       int
	errs                        int
}

func (c *countRec) OnOpen(_ string, _ vfs.OpenFlags, _ time.Duration, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens++
	c.note(err)
}
func (c *countRec) OnRead(_ string, _ int64, n int, _ time.Duration, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	c.readBytes += n
	c.note(err)
}
func (c *countRec) OnWrite(_ string, _ int64, n int, _ time.Duration, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes++
	c.writeBytes += n
	c.note(err)
}
func (c *countRec) OnSync(_ string, _ time.Duration, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.syncs++
	c.note(err)
}

// note counts only genuine faults — io.EOF is SQLite's expected
// short-read signal, not an error.
func (c *countRec) note(err error) {
	if err != nil && !errors.Is(err, io.EOF) {
		c.errs++
	}
}

func TestUserVFS_Wrap(t *testing.T) {
	name := uniqueVFSName("refwrap")
	rec := &countRec{}
	if err := vfs.Register(name, vfs.Wrap(newRefMemVFS(), rec)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _ = vfs.Unregister(name) })

	db, _ := sql.Open(sqlite.DriverName, "file:w.db?vfs="+name)
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(x); INSERT INTO t VALUES (1),(2),(3)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("count=%d, want 3", n)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.opens == 0 {
		t.Error("no opens recorded")
	}
	if rec.writes == 0 || rec.writeBytes == 0 {
		t.Errorf("writes=%d writeBytes=%d, want both > 0", rec.writes, rec.writeBytes)
	}
	if rec.reads == 0 {
		t.Error("no reads recorded")
	}
	if rec.syncs == 0 {
		t.Error("no syncs recorded (expected fsync on commit in journal mode)")
	}
	if rec.errs != 0 {
		t.Errorf("recorded %d genuine errors, want 0", rec.errs)
	}
}

func TestUserVFS_WrapNilRecorder(t *testing.T) {
	base := newRefMemVFS()
	if got := vfs.Wrap(base, nil); got != vfs.VFS(base) {
		t.Error("Wrap(base, nil) should return base unchanged")
	}
}

func TestUserVFS_WrapSlogRecorder(t *testing.T) {
	name := uniqueVFSName("refslog")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	impl := vfs.Wrap(newRefMemVFS(), vfs.NewSlogRecorder(logger))
	if err := vfs.Register(name, impl); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() { _ = vfs.Unregister(name) })

	db, _ := sql.Open(sqlite.DriverName, "file:s.db?vfs="+name)
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(x)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("slog recorder emitted nothing at Debug level")
	}
	if !strings.Contains(buf.String(), "vfs.") {
		t.Errorf("expected vfs.* events in log, got:\n%s", buf.String())
	}
}
