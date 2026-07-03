// Copyright 2025 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0 license that can be
// found in the LICENSE file.

package sqlite // import "gosqlite.org"

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"sync"
)

// remoteOpeners maps a lower-cased URL scheme to a function that opens a
// driver.Conn for a DSN of that scheme. It lets an out-of-tree package teach the
// built-in "sqlite"/"sqlite3" driver to reach a network backend without the root
// taking on that backend's dependencies.
var (
	remoteMu      sync.RWMutex
	remoteOpeners = map[string]func(string) (driver.Conn, error){}
)

// RegisterRemoteScheme teaches the built-in "sqlite"/"sqlite3" driver to open a
// DSN whose URL scheme is scheme by forwarding it to opener, instead of treating
// the DSN as a local database file. It is the seam a network driver — the
// quicSQL forwarding driver — uses from its init() so that
//
//	sql.Open("sqlite", "quicsql://host/db?token=…")
//
// reaches a remote server while ordinary file DSNs continue to open in-process.
// The root gains no network dependency of its own: opener and the transports it
// builds live in the caller's package, and enter a program's build only when
// that package is imported. A remote conn skips all local-engine setup
// (Extensions, ConnectHook, registered functions/collations, vtab modules) —
// those have no meaning against a server that runs the engine itself.
//
// scheme is matched case-insensitively against the text before "://" in the DSN.
// The reserved "file" scheme and an empty scheme panic, as does registering the
// same scheme twice. Safe to call from init(); it must run before the first
// sql.Open that uses the scheme.
func RegisterRemoteScheme(scheme string, opener func(dsn string) (driver.Conn, error)) {
	if opener == nil {
		panic("sqlite: RegisterRemoteScheme: opener is nil")
	}
	s := strings.ToLower(scheme)
	if s == "" || s == "file" {
		panic(fmt.Sprintf("sqlite: RegisterRemoteScheme: invalid scheme %q", scheme))
	}
	remoteMu.Lock()
	defer remoteMu.Unlock()
	if _, dup := remoteOpeners[s]; dup {
		panic(fmt.Sprintf("sqlite: RegisterRemoteScheme: scheme %q already registered", scheme))
	}
	remoteOpeners[s] = opener
}

// dsnScheme returns the lower-cased URL scheme of dsn (the text before "://") and
// whether dsn is URL-shaped in that sense. Local DSNs never match: a bare path
// and ":memory:" contain no "://", and while a "file://" URI does, its scheme is
// "file", which is reserved and handled locally.
func dsnScheme(dsn string) (string, bool) {
	i := strings.Index(dsn, "://")
	if i <= 0 {
		return "", false
	}
	return strings.ToLower(dsn[:i]), true
}

// remoteOpenerFor resolves the opener for a URL-shaped, non-file DSN. It returns
// (opener, true) when the DSN targets a remote scheme (whether or not an opener
// is registered — a nil opener means "remote-shaped but nobody registered it",
// which Driver.Open turns into a helpful error) and (nil, false) for a local DSN.
func remoteOpenerFor(dsn string) (func(string) (driver.Conn, error), bool) {
	scheme, ok := dsnScheme(dsn)
	if !ok || scheme == "file" {
		return nil, false
	}
	remoteMu.RLock()
	opener := remoteOpeners[scheme]
	remoteMu.RUnlock()
	return opener, true
}
