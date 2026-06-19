// Package testhelp consolidates the *sql.DB / *sql.Conn fixture
// boilerplate shared across the module's tests. The duplicates it
// replaces all do the same things: pin MaxOpenConns to 1, grab a
// single *sql.Conn, register a per-conn vtab or hook, and clean up
// in the right order.
package testhelp

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	sqlite "gosqlite.org"
)

// OpenPinned opens an in-memory *sql.DB on the named driver (typically
// "sqlite"), pins MaxOpenConns to 1, grabs a single *sql.Conn, and
// registers t.Cleanup for both. Use this when a test needs a single
// stable connection — typically because something stateful is being
// installed on it (a vtab, a hook, a BLOB handle).
//
// The dsn is forwarded verbatim. Common values: ":memory:" (default
// in-memory), "file:foo?mode=memory&cache=shared" (sharable memory),
// or an on-disk path.
func OpenPinned(t *testing.T, driverName, dsn string) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("testhelp: sql.Open(%q, %q): %v", driverName, dsn, err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("testhelp: db.Conn: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	return db, sc
}

// RawConn extracts the *sqlite.Conn underlying a *sql.Conn. Use this
// to call per-connection methods (RegisterFunction, OpenBlob, etc.)
// from inside a test.
func RawConn(t *testing.T, sc *sql.Conn) *sqlite.Conn {
	t.Helper()
	var out *sqlite.Conn
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		out = c
		return nil
	}); err != nil {
		t.Fatalf("testhelp: sc.Raw: %v", err)
	}
	return out
}

// RegisterOn calls reg(c) under sc.Raw and fatals the test if anything
// fails. Use when a test needs to install a per-conn vtab module or
// scalar function on the single pinned connection.
func RegisterOn(t *testing.T, sc *sql.Conn, reg func(*sqlite.Conn) error) {
	t.Helper()
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return reg(c)
	}); err != nil {
		t.Fatalf("testhelp: register: %v", err)
	}
}

// WithConnectHook installs reg as a ConnectHook on the default driver
// for the duration of the test. The previous hook (if any) is invoked
// first; t.Cleanup restores it. Use this when a test wants the hook
// to fire on every conn the pool opens, not just one pinned conn —
// typically because the test eventually relies on database/sql's
// automatic conn dispatch.
//
// Pair with OpenPinned for tests that combine pool-wide registration
// with single-conn pinning (e.g. statement / pivot / closure vtab
// tests).
func WithConnectHook(t *testing.T, reg func(*sqlite.Conn) error) {
	t.Helper()
	d := sqlite.DefaultDriver()
	prev := d.ConnectHook
	d.ConnectHook = func(c *sqlite.Conn) error {
		if prev != nil {
			if err := prev(c); err != nil {
				return err
			}
		}
		return reg(c)
	}
	t.Cleanup(func() { d.ConnectHook = prev })
}
