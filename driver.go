// Copyright 2025 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0 license that can be
// found in the LICENSE file.

package sqlite // import "gosqlite.org"

import (
	"database/sql/driver"
	"fmt"
	"maps"
	"sync"

	"modernc.org/sqlite/vtab"
)

// Driver implements database/sql/driver.Driver.
//
// Registration functions and methods must be called before the first call to Open.
//
// For drop-in compatibility with github.com/mattn/go-sqlite3, the type alias
// SQLiteDriver and the public fields Extensions and ConnectHook are provided
// so existing mattn code that does
//
//	sql.Register("custom", &sqlite3.SQLiteDriver{
//	    Extensions:  []string{"my_ext"},
//	    ConnectHook: func(c *sqlite3.SQLiteConn) error { ... },
//	})
//
// continues to work unchanged with this package.
type Driver struct {
	// Extensions is a list of SQLite extension shared-library paths to load
	// into every new connection via sqlite3_load_extension. Loaded after the
	// connection is opened but before user ConnectHook fires. Mattn-compatible.
	Extensions []string

	// ConnectHook, if non-nil, is invoked once for every newly-opened
	// connection, after Extensions are loaded and after the modernc-style
	// connection hooks run. Mattn-compatible.
	ConnectHook func(*Conn) error

	// mu guards the mutable maps and slice below from concurrent
	// Register* calls racing against Open's iteration.
	mu sync.RWMutex
	// user defined functions that are added to every new connection on Open
	udfs map[string]*userDefinedFunction
	// collations that are added to every new connection on Open
	collations map[string]*collation
	// connection hooks are called after a connection is opened
	connectionHooks []ConnectionHookFn
	// modules holds registered virtual table modules that should be added to
	// every new connection on Open.
	modules map[string]vtab.Module
}

var d = &Driver{
	udfs:            make(map[string]*userDefinedFunction, 0),
	collations:      make(map[string]*collation, 0),
	connectionHooks: make([]ConnectionHookFn, 0),
	modules:         make(map[string]vtab.Module, 0),
}

func newDriver() *Driver { return d }

// DefaultDriver returns the singleton [*Driver] the package registers
// under both "sqlite" and "sqlite3". Use this when a sub-package needs to
// install a [Driver.ConnectHook] from an init() function — for example,
// the `ext/<name>/auto/` blank-import sub-packages register their module
// on every new connection by chaining onto the existing ConnectHook.
//
// Callers MUST preserve any previously-installed hook by invoking the
// prior value before running their own work. Assigning
// DefaultDriver().ConnectHook = … without saving and chaining discards
// the existing hook silently.
func DefaultDriver() *Driver { return d }

// Open returns a new connection to the database. The name is a string in a
// driver-specific format.
//
// Open may return a cached connection (one previously closed), but doing so is
// unnecessary; the sql package maintains a pool of idle connections for
// efficient re-use.
//
// The returned connection is only used by one goroutine at a time.
//
// The name may be a filename, e.g., "/tmp/mydata.sqlite", or a URI, in which
// case it may include a '?' followed by one or more query parameters.
// For example, "file:///tmp/mydata.sqlite?_pragma=foreign_keys(1)&_time_format=sqlite".
// The supported query parameters are:
//
// _pragma: Each value will be run as a "PRAGMA ..." statement (with the PRAGMA
// keyword added for you). May be specified more than once, '&'-separated. For more
// information on supported PRAGMAs see: https://www.sqlite.org/pragma.html
//
// _time_format: The name of a format to use when writing time values to the database.
// The currently supported values are (1) "sqlite" for YYYY-MM-DD HH:MM:SS.SSS[+-]HH:MM
// (format 4 from https://www.sqlite.org/lang_datefunc.html#time_values with sub-second
// precision and timezone specifier) and (2) "datetime" for YYYY-MM-DD HH:MM:SS
// (format 3, matching the output of SQLite's datetime() function).
// If this parameter is not specified, then the default String() format will be used.
//
// _time_integer_format: The name of a integer format to use when writing time values.
// By default, the time is stored as string and the format can be set with _time_format
// parameter. If _time_integer_format is set, the time will be stored as an integer and
// the integer value will depend on the integer format.
// If you decide to set both _time_format and _time_integer_format, the time will be
// converted as integer and the _time_format value will be ignored.
// Currently the supported value are "unix","unix_milli", "unix_micro" and "unix_nano",
// which corresponds to seconds, milliseconds, microseconds or nanoseconds
// since unixepoch (1 January 1970 00:00:00 UTC).
//
// _inttotime: Enable conversion of time column (DATE, DATETIME,TIMESTAMP) from integer
// to time if the field contain integer (int64).
//
// _texttotime: Enable ColumnTypeScanType to report time.Time instead of string
// for TEXT columns declared as DATE, DATETIME, TIME, or TIMESTAMP.
//
// _timezone: A timezone to use for all time reads and writes, such as "UTC".
// The value is parsed by time.LoadLocation.
// Writes will convert to the timezone before formatting as a string;
// it does not impact _inttotime integer values, as they always use UTC.
// Reads will interpret timezone-less strings as being in this timezone.
// Values that are in a known timezone, such as a string with a timezone specifier
// or an integer with _inttotime (specified to be in UTC), will be converted to this timezone.
//
// _txlock: The locking behavior to use when beginning a transaction. May be
// "deferred" (the default), "immediate", or "exclusive" (case insensitive). See:
// https://www.sqlite.org/lang_transaction.html#deferred_immediate_and_exclusive_transactions
func (d *Driver) Open(name string) (conn driver.Conn, err error) {
	if dmesgs {
		defer func() {
			dmesg("name %q: (driver.Conn %p, err %v)", name, conn, err)
		}()
	}
	c, err := newConn(name)
	if err != nil {
		return nil, err
	}

	// Snapshot the four driver-mutated collections under d.mu so a
	// concurrent Register* call can't race against this Open.
	d.mu.RLock()
	udfs := make([]*userDefinedFunction, 0, len(d.udfs))
	for _, u := range d.udfs {
		udfs = append(udfs, u)
	}
	colls := make([]*collation, 0, len(d.collations))
	for _, coll := range d.collations {
		colls = append(colls, coll)
	}
	hooks := make([]ConnectionHookFn, len(d.connectionHooks))
	copy(hooks, d.connectionHooks)
	mods := make(map[string]vtab.Module, len(d.modules))
	maps.Copy(mods, d.modules)
	d.mu.RUnlock()

	for _, udf := range udfs {
		if err = c.createFunctionInternal(udf); err != nil {
			c.Close()
			return nil, err
		}
	}
	for _, coll := range colls {
		if err = c.createCollationInternal(coll); err != nil {
			c.Close()
			return nil, err
		}
	}
	for _, connHookFn := range hooks {
		if err = connHookFn(c, name); err != nil {
			c.Close()
			return nil, fmt.Errorf("connection hook: %w", err)
		}
	}
	// Register any vtab modules with this connection.
	// Note: vtab module registration applies to new connections only. If a
	// module is registered after a connection has been opened, that existing
	// connection will not see the module; open a new connection to use it.
	for name, mod := range mods {
		if err := c.registerSingleModule(name, mod); err != nil {
			c.Close()
			return nil, err
		}
	}

	// Mattn-compat: load any driver-level extensions, then run ConnectHook.
	if len(d.Extensions) > 0 {
		if err := c.enableLoadExtension(true); err != nil {
			c.Close()
			return nil, fmt.Errorf("enable load extension: %w", err)
		}
		for _, ext := range d.Extensions {
			if err := c.loadExtension(ext, ""); err != nil {
				c.Close()
				return nil, fmt.Errorf("load extension %q: %w", ext, err)
			}
		}
	}
	// Snapshot ConnectHook under d.mu so RegisterAutoHook's concurrent
	// write doesn't race with the read (the race detector flags the
	// otherwise-atomic pointer load otherwise). Invoke outside the lock
	// so a slow hook can't block other connection opens.
	d.mu.Lock()
	hook := d.ConnectHook
	d.mu.Unlock()
	if hook != nil {
		if err := hook(c); err != nil {
			c.Close()
			return nil, fmt.Errorf("connect hook: %w", err)
		}
	}
	return c, nil
}

// RegisterConnectionHook registers a function to be called after each connection
// is opened. This is called after all the connection has been set up.
func (d *Driver) RegisterConnectionHook(fn ConnectionHookFn) {
	d.mu.Lock()
	d.connectionHooks = append(d.connectionHooks, fn)
	d.mu.Unlock()
}

// RegisterAutoHook chains a Register-style hook onto the default
// driver's [Driver.ConnectHook], preserving any previously-installed
// hook. The hook fires on every newly-opened connection, after the
// modernc-style ConnectionHooks and Mattn-style Extensions have run.
//
// This is the recipe the blank-import `ext/<name>/auto` sub-packages
// use to opt every connection in to a SQL extension without
// trampling whichever ConnectHook was already there. Composing many
// /auto imports just chains them in import-graph order — each call
// captures the previous chain and prepends a run-prior-then-reg
// trampoline.
//
//	func init() {
//	    sqlite.RegisterAutoHook(myext.Register)
//	}
//
// Concurrency: safe to call from init() (single goroutine) or after.
// Both the write here and the read in [Driver.Open] are guarded by
// d.mu, so concurrent registration and connection-opening compose
// without races. Pool-wide consistency requires installing all hooks
// before the first sql.Open, which the blank-import pattern
// guarantees.
func RegisterAutoHook(reg func(*Conn) error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	prev := d.ConnectHook
	d.ConnectHook = func(c *Conn) error {
		if prev != nil {
			if err := prev(c); err != nil {
				return err
			}
		}
		return reg(c)
	}
}
