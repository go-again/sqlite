// Copyright 2025 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0 license that can be
// found in the LICENSE file.

package sqlite

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// fakeRemoteConn is a stand-in driver.Conn returned by a registered opener.
type fakeRemoteConn struct{ dsn string }

func (fakeRemoteConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (fakeRemoteConn) Close() error                        { return nil }
func (fakeRemoteConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }

func TestDSNScheme(t *testing.T) {
	cases := []struct {
		dsn    string
		scheme string
		ok     bool
	}{
		{":memory:", "", false},
		{"file:test.db", "", false},
		{"file:test.db?cache=shared&mode=memory", "", false},
		{"/var/lib/app.db", "", false},
		{"./rel/path.db", "", false},
		{"file:///tmp/x.db", "file", true}, // URL-shaped but reserved → handled locally
		{"quicsql://host:7801/db?token=x", "quicsql", true},
		{"H2C://Host/DB", "h2c", true}, // case-insensitive
	}
	for _, c := range cases {
		got, ok := dsnScheme(c.dsn)
		if got != c.scheme || ok != c.ok {
			t.Errorf("dsnScheme(%q) = (%q, %v), want (%q, %v)", c.dsn, got, ok, c.scheme, c.ok)
		}
	}
	// A file:// URI must never be treated as remote, even though it is URL-shaped.
	if _, remote := remoteOpenerFor("file:///tmp/x.db"); remote {
		t.Error("file:// URI treated as remote")
	}
	if _, remote := remoteOpenerFor(":memory:"); remote {
		t.Error(":memory: treated as remote")
	}
}

func TestRegisterRemoteSchemeDispatch(t *testing.T) {
	var seen string
	RegisterRemoteScheme("faketest", func(dsn string) (driver.Conn, error) {
		seen = dsn
		return fakeRemoteConn{dsn: dsn}, nil
	})

	const dsn = "faketest://host:9/appdb?token=abc"
	c, err := newDriver().Open(dsn)
	if err != nil {
		t.Fatalf("Open(%q): %v", dsn, err)
	}
	if fc, ok := c.(fakeRemoteConn); !ok || fc.dsn != dsn {
		t.Fatalf("Open returned %#v, want fakeRemoteConn{%q}", c, dsn)
	}
	if seen != dsn {
		t.Fatalf("opener saw %q, want the full DSN %q", seen, dsn)
	}
}

func TestUnregisteredRemoteSchemeHint(t *testing.T) {
	_, err := newDriver().Open("quicsqlmissing://h/db")
	if err == nil {
		t.Fatal("Open of an unregistered remote scheme should error")
	}
	if !strings.Contains(err.Error(), "quicsqlmissing") || !strings.Contains(err.Error(), "import") {
		t.Fatalf("error is not helpful: %v", err)
	}
}

func TestLocalDSNNotIntercepted(t *testing.T) {
	// An in-memory DSN must still open the local engine, not the remote path.
	c, err := newDriver().Open(":memory:")
	if err != nil {
		t.Fatalf("open :memory:: %v", err)
	}
	if _, ok := c.(fakeRemoteConn); ok {
		t.Fatal(":memory: was wrongly dispatched to a remote opener")
	}
	c.Close()
}

func TestRegisterRemoteSchemePanics(t *testing.T) {
	op := func(string) (driver.Conn, error) { return nil, nil }
	mustPanic(t, "nil opener", func() { RegisterRemoteScheme("nilop", nil) })
	mustPanic(t, "empty scheme", func() { RegisterRemoteScheme("", op) })
	mustPanic(t, "reserved file scheme", func() { RegisterRemoteScheme("file", op) })

	RegisterRemoteScheme("dupscheme", op)
	mustPanic(t, "duplicate scheme", func() { RegisterRemoteScheme("dupscheme", op) })
}

func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic, got none", what)
		}
	}()
	fn()
}
