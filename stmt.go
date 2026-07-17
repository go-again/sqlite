// Copyright 2025 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by the Apache 2.0 license that can be
// found in the LICENSE file.

package sqlite // import "gosqlite.org"

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

type stmt struct {
	c     *conn
	psql  uintptr
	pstmt uintptr // The cached SQLite statement handle

	// cacheKey is set to the normalized SQL when this *stmt is eligible for
	// donation back to c.stmts on Close (single-statement, non-script, with
	// caching enabled). Empty cacheKey means Close should finalize + free.
	cacheKey string
}

func newStmt(c *conn, sql string) (*stmt, error) {
	// Fast path: cache hit. Take the cached entry, reset its pstmt + clear
	// any leftover bindings, and hand it back wrapped in a *stmt whose Close
	// will donate it back to the cache instead of finalizing.
	if c.stmts != nil && c.stmts.enabled() {
		if entry := c.stmts.take(sql); entry != nil {
			// Reset to a clean state — required before binding new params.
			// We deliberately ignore reset's return value: even if the
			// previous execution finished with SQLITE_ERROR, reset clears
			// the error state, which is what we want.
			sqlite3.Xsqlite3_reset(c.tls, entry.pstmt)
			sqlite3.Xsqlite3_clear_bindings(c.tls, entry.pstmt)
			return &stmt{
				c:        c,
				psql:     entry.psql,
				pstmt:    entry.pstmt,
				cacheKey: entry.key,
			}, nil
		}
	}

	// Cache miss (or cache disabled). Allocate + prepare the usual way.
	p, err := libc.CString(sql)
	if err != nil {
		return nil, err
	}
	s := &stmt{c: c, psql: p}

	psql := p
	pstmt, err := c.prepareV2(&psql)
	if err != nil {
		c.free(p)
		return nil, err
	}

	// If we consumed all input we have a single statement we can cache.
	// Otherwise this is a script (semicolon-separated) or a comment-only
	// blob; those aren't eligible for caching.
	hasTail := *(*byte)(unsafe.Pointer(psql)) != 0
	if pstmt != 0 && !hasTail {
		s.pstmt = pstmt
		if c.stmts != nil && c.stmts.enabled() {
			s.cacheKey = normalize(sql)
		}
		return s, nil
	}

	// Script or comment-only: discard the partial prepare and let
	// exec/query re-parse iteratively.
	if pstmt != 0 {
		if err := c.finalize(pstmt); err != nil {
			c.free(p)
			return nil, err
		}
	}
	return s, nil
}

// Close closes the statement. If the *stmt is eligible for caching, the
// underlying prepared-statement handle is reset and donated to c.stmts
// instead of being finalized; the next Prepare with the same SQL will
// pull it back out cheaply.
//
// As of Go 1.1, a Stmt will not be closed if it's in use by any queries.
func (s *stmt) Close() (err error) {
	if s.pstmt != 0 && s.cacheKey != "" && s.c.stmts != nil && s.c.stmts.enabled() {
		// Donate back. Reset clears any execution state; clear_bindings
		// releases any blob/text bindings the user may have set so the
		// retained pstmt doesn't hold ref counts on caller memory.
		sqlite3.Xsqlite3_reset(s.c.tls, s.pstmt)
		sqlite3.Xsqlite3_clear_bindings(s.c.tls, s.pstmt)
		if evicted := s.c.stmts.put(s.cacheKey, s.psql, s.pstmt); evicted != nil {
			// Cache was full or the same key was re-inserted; finalize the
			// loser. Ignore the finalize error here — the new entry is in
			// the cache and the user already got the success they expected.
			_ = s.c.finalize(evicted.pstmt)
			s.c.free(evicted.psql)
		}
		// Ownership of psql + pstmt has been transferred to the cache.
		s.pstmt = 0
		s.psql = 0
		return nil
	}

	if s.pstmt != 0 {
		if e := s.c.finalize(s.pstmt); e != nil {
			err = e
		}
		s.pstmt = 0
	}
	if s.psql != 0 {
		s.c.free(s.psql)
		s.psql = 0
	}
	return err
}

// Exec executes a query that doesn't return rows, such as an INSERT or UPDATE.
//
// Deprecated: Drivers should implement StmtExecContext instead (or
// additionally).
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) { //TODO StmtExecContext
	return s.exec(context.Background(), toNamedValues(args))
}

// toNamedValues converts []driver.Value to []driver.NamedValue
func toNamedValues(vals []driver.Value) (r []driver.NamedValue) {
	r = make([]driver.NamedValue, len(vals))
	for i, val := range vals {
		r[i] = driver.NamedValue{Value: val, Ordinal: i + 1}
	}
	return r
}

func (s *stmt) exec(ctx context.Context, args []driver.NamedValue) (r driver.Result, err error) {
	var pstmt uintptr
	var done int32
	if ctx != nil {
		if ctxDone := ctx.Done(); ctxDone != nil {
			select {
			case <-ctxDone:
				return nil, ctx.Err()
			default:
			}
			defer interruptOnDone(ctx, s.c, &done)()
		}
	}

	defer func() {
		if ctx != nil && atomic.LoadInt32(&done) != 0 {
			r, err = nil, ctx.Err()
		}
		if pstmt != 0 {
			// ensure stmt finalized.
			e := s.c.finalize(pstmt)

			if err == nil && e != nil {
				// prioritize original
				// returned error.
				err = e
			}
		}
	}()

	// OPTIMIZED PATH: Single Cached Statement
	if s.pstmt != 0 {
		err = func() error {
			// Bind
			n, err := s.c.bindParameterCount(s.pstmt)
			if err != nil {
				return err
			}
			if n != 0 {
				allocs, err := s.c.bind(s.pstmt, n, args)
				if err != nil {
					return err
				}
				// Free allocations after step
				if len(allocs) != 0 {
					defer func() { s.c.freeAllocs(allocs) }()
				}
			}

			// Step
			rc, err := s.c.step(s.pstmt)
			if err != nil {
				return err
			}

			// Handle Result
			switch rc & 0xff {
			case sqlite3.SQLITE_DONE:
				r, err = newResult(s.c)
			case sqlite3.SQLITE_ROW:
				// Step to completion, matching C sqlite3_exec()
				// semantics. Required for DML RETURNING correctness;
				// also drains SELECT results if passed to Exec.
				for rc&0xff == sqlite3.SQLITE_ROW {
					if atomic.LoadInt32(&done) != 0 {
						return ctx.Err()
					}
					rc, err = s.c.step(s.pstmt)
					if err != nil {
						return err
					}
				}
				if rc&0xff != sqlite3.SQLITE_DONE {
					return s.c.errstr(int32(rc))
				}
				r, err = newResult(s.c)
			default:
				return s.c.errstr(int32(rc))
			}
			return err
		}()

		// RESET (Crucial: Do not finalize)
		// We must reset the VM to allow reuse.
		// We also clear bindings to prevent leaking memory or state to next call.
		if resetErr := s.c.reset(s.pstmt); resetErr != nil && err == nil {
			err = resetErr
		}
		if clearErr := s.c.clearBindings(s.pstmt); clearErr != nil && err == nil {
			err = clearErr
		}
		return r, err
	}

	// FALLBACK PATH: Multi-statement script
	for psql := s.psql; *(*byte)(unsafe.Pointer(psql)) != 0 && atomic.LoadInt32(&done) == 0; {
		if pstmt, err = s.c.prepareV2(&psql); err != nil {
			return nil, err
		}

		if pstmt == 0 {
			continue
		}
		err = func() error {
			n, err := s.c.bindParameterCount(pstmt)
			if err != nil {
				return err
			}

			if n != 0 {
				allocs, err := s.c.bind(pstmt, n, args)
				if err != nil {
					return err
				}

				if len(allocs) != 0 {
					defer func() { s.c.freeAllocs(allocs) }()
				}
			}

			rc, err := s.c.step(pstmt)
			if err != nil {
				return err
			}

			switch rc & 0xff {
			case sqlite3.SQLITE_DONE:
				r, err = newResult(s.c)
			case sqlite3.SQLITE_ROW:
				// Step to completion, matching C sqlite3_exec()
				// semantics. Required for DML RETURNING correctness;
				// also drains SELECT results if passed to Exec.
				for rc&0xff == sqlite3.SQLITE_ROW {
					if atomic.LoadInt32(&done) != 0 {
						return ctx.Err()
					}
					rc, err = s.c.step(pstmt)
					if err != nil {
						return err
					}
				}
				if rc&0xff != sqlite3.SQLITE_DONE {
					return s.c.errstr(int32(rc))
				}
				r, err = newResult(s.c)
			default:
				return s.c.errstr(int32(rc))
			}

			return err
		}()

		e := s.c.finalize(pstmt)
		pstmt = 0 // done with

		if err == nil && e != nil {
			// prioritize original
			// returned error.
			err = e
		}

		if err != nil {
			return nil, err
		}
	}
	return r, err
}

// NumInput returns the number of placeholder parameters.
//
// If NumInput returns >= 0, the sql package will sanity check argument counts
// from callers and return errors to the caller before the statement's Exec or
// Query methods are called.
//
// NumInput may also return -1, if the driver doesn't know its number of
// placeholders. In that case, the sql package will not sanity check Exec or
// Query argument counts.
func (s *stmt) NumInput() (n int) {
	return -1
}

// Query executes a query that may return rows, such as a
// SELECT.
//
// Deprecated: Drivers should implement StmtQueryContext instead (or
// additionally).
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) { //TODO StmtQueryContext
	return s.query(context.Background(), toNamedValues(args))
}

// query backs Stmt.Query / Stmt.QueryContext. The ctx-watcher lifecycle
// note: interruptOnDone spawns a goroutine that fires sqlite3_interrupt
// when ctx is canceled; its cleanup runs when query() returns. Once
// *rows is handed to database/sql, the watcher is gone, and SQL
// interrupt is no longer fired from our side — database/sql owns the
// row-iteration ctx and closes the rows on cancel. Vtab Filter loops
// that need mid-iteration cancellability must poll
// (*Conn).IsInterrupted between SQLite calls.
func (s *stmt) query(ctx context.Context, args []driver.NamedValue) (r driver.Rows, err error) {
	var pstmt uintptr
	var done int32
	if ctx != nil {
		if ctxDone := ctx.Done(); ctxDone != nil {
			select {
			case <-ctxDone:
				return nil, ctx.Err()
			default:
			}
			defer interruptOnDone(ctx, s.c, &done)()
		}
	}

	defer func() {
		if ctx != nil && atomic.LoadInt32(&done) != 0 {
			if r != nil {
				r.Close()
			}
			r, err = nil, ctx.Err()
		} else if r == nil && err == nil {
			r, err = newRows(s.c, pstmt, nil, true)
		}

		if pstmt != 0 {
			// ensure stmt finalized.
			e := s.c.finalize(pstmt)

			if err == nil && e != nil {
				// prioritize original
				// returned error.
				err = e
			}
		}

	}()

	// OPTIMIZED PATH: Single Cached Statement
	if s.pstmt != 0 {
		var allocs []uintptr
		// Bind
		n, err := s.c.bindParameterCount(s.pstmt)
		if err != nil {
			return nil, err
		}
		if n != 0 {
			if allocs, err = s.c.bind(s.pstmt, n, args); err != nil {
				return nil, err
			}
		}

		// Step
		rc, err := s.c.step(s.pstmt)
		if err != nil {
			// On error, we must free allocs manually because 'newRows' won't take ownership
			s.c.freeAllocs(allocs)
			s.c.reset(s.pstmt)
			s.c.clearBindings(s.pstmt)
			return nil, err
		}

		// Handle Result
		switch rc & 0xff {
		case sqlite3.SQLITE_ROW:
			// Pass reuseStmt=true
			if r, err = newRows(s.c, s.pstmt, &allocs, false); err != nil {
				s.c.reset(s.pstmt)
				s.c.clearBindings(s.pstmt)
				return nil, err
			}
			r.(*rows).reuseStmt = true
			return r, nil

		case sqlite3.SQLITE_DONE:
			// No rows. Reset immediately.
			// We still return a rows object (empty), but we can reset the stmt now
			// because the empty rows object won't call step() again.
			// However, standard newRows behavior expects a valid stmt to get columns.
			// Let's rely on newRows to read columns, then it returns.

			// Actually, if we pass reuseStmt=true to an empty set,
			// rows.Close() will eventually reset it.
			if r, err = newRows(s.c, s.pstmt, &allocs, true); err != nil {
				s.c.reset(s.pstmt)
				s.c.clearBindings(s.pstmt)
				return nil, err
			}
			r.(*rows).reuseStmt = true
			return r, nil

		default:
			// Error case
			s.c.freeAllocs(allocs)
			s.c.reset(s.pstmt)
			s.c.clearBindings(s.pstmt)
			return nil, s.c.errstr(int32(rc))
		}
	}

	// FALLBACK PATH: Multi-statement script
	for psql := s.psql; *(*byte)(unsafe.Pointer(psql)) != 0 && atomic.LoadInt32(&done) == 0; {
		if pstmt, err = s.c.prepareV2(&psql); err != nil {
			if r != nil {
				r.Close()
			}
			return nil, err
		}

		if pstmt == 0 {
			continue
		}

		err = func() (err error) {
			var allocs []uintptr
			defer func() { s.c.freeAllocs(allocs) }()

			n, err := s.c.bindParameterCount(pstmt)
			if err != nil {
				return err
			}

			if n != 0 {
				if allocs, err = s.c.bind(pstmt, n, args); err != nil {
					return err
				}
			}

			rc, err := s.c.step(pstmt)
			if err != nil {
				return err
			}

			switch rc & 0xff {
			case sqlite3.SQLITE_ROW:
				if r != nil {
					r.Close()
				}
				if r, err = newRows(s.c, pstmt, &allocs, false); err != nil {
					return err
				}
				pstmt = 0
				return nil
			case sqlite3.SQLITE_DONE:
				if r == nil {
					if r, err = newRows(s.c, pstmt, &allocs, true); err != nil {
						return err
					}
					pstmt = 0
					return nil
				}

				// nop
			default:
				return s.c.errstr(int32(rc))
			}

			if *(*byte)(unsafe.Pointer(psql)) == 0 {
				if r != nil {
					r.Close()
				}
				if r, err = newRows(s.c, pstmt, &allocs, true); err != nil {
					return err
				}
				pstmt = 0
			}
			return nil
		}()

		e := s.c.finalize(pstmt)
		pstmt = 0 // done with

		if err == nil && e != nil {
			// prioritize original
			// returned error.
			err = e
		}

		if err != nil {
			if r != nil {
				r.Close() // r is from a previous iteration; clean up since we won't return it
			}
			return nil, err
		}
	}
	return r, err
}

// ExecContext implements driver.StmtExecContext
func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (dr driver.Result, err error) {
	if dmesgs {
		defer func() {
			dmesg("stmt %p, ctx %p, args %v: (driver.Result %p, err %v)", s, ctx, args, dr, err)
		}()
	}
	return s.exec(ctx, args)
}

// QueryContext implements driver.StmtQueryContext
func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (dr driver.Rows, err error) {
	if dmesgs {
		defer func() {
			dmesg("stmt %p, ctx %p, args %v: (driver.Rows %p, err %v)", s, ctx, args, dr, err)
		}()
	}
	return s.query(ctx, args)
}

// C documentation
//
//	int sqlite3_clear_bindings(sqlite3_stmt*);
func (c *conn) clearBindings(pstmt uintptr) error {
	if rc := sqlite3.Xsqlite3_clear_bindings(c.tls, pstmt); rc != sqlite3.SQLITE_OK {
		return c.errstr(rc)
	}
	return nil
}

// ColumnCount returns the number of result columns the statement
// produces, equivalent to sqlite3_column_count. Useful from inside
// vtab xCreate callbacks (ext/statement, ext/pivot) where the schema
// has to be discovered before any row is stepped.
func (s *stmt) ColumnCount() int {
	if s.pstmt == 0 {
		return 0
	}
	return int(sqlite3.Xsqlite3_column_count(s.c.tls, s.pstmt))
}

// ColumnName returns the result column name at the given 0-based
// index, equivalent to sqlite3_column_name. Returns "" for out-of-
// range indices.
func (s *stmt) ColumnName(i int) string {
	if s.pstmt == 0 {
		return ""
	}
	p := sqlite3.Xsqlite3_column_name(s.c.tls, s.pstmt, int32(i))
	if p == 0 {
		return ""
	}
	return libc.GoString(p)
}

// ColumnDeclType returns the declared SQL type of the result column at
// the given 0-based index, equivalent to sqlite3_column_decltype.
// Returns "" for expression columns (no underlying declared type) or
// out-of-range indices.
func (s *stmt) ColumnDeclType(i int) string {
	if s.pstmt == 0 {
		return ""
	}
	p := sqlite3.Xsqlite3_column_decltype(s.c.tls, s.pstmt, int32(i))
	if p == 0 {
		return ""
	}
	return libc.GoString(p)
}

// BindCount returns the number of bound parameters in the statement,
// equivalent to sqlite3_bind_parameter_count.
func (s *stmt) BindCount() int {
	if s.pstmt == 0 {
		return 0
	}
	return int(sqlite3.Xsqlite3_bind_parameter_count(s.c.tls, s.pstmt))
}

// BindName returns the name of the bound parameter at the given
// 1-based index (matching SQLite's convention), or "" for anonymous
// `?` parameters. Equivalent to sqlite3_bind_parameter_name.
func (s *stmt) BindName(i int) string {
	if s.pstmt == 0 {
		return ""
	}
	p := sqlite3.Xsqlite3_bind_parameter_name(s.c.tls, s.pstmt, int32(i))
	if p == 0 {
		return ""
	}
	return libc.GoString(p)
}

// Readonly reports whether the prepared statement makes no direct changes to
// the database file — useful for routing reads vs writes. Equivalent to
// sqlite3_stmt_readonly; a finalized statement is reported read-only.
func (s *stmt) Readonly() bool {
	if s.pstmt == 0 {
		return true
	}
	return sqlite3.Xsqlite3_stmt_readonly(s.c.tls, s.pstmt) != 0
}

// StmtStatus selects a per-statement counter for [Stmt.Status].
type StmtStatus int32

// Per-prepared-statement counters (sqlite3_stmt_status).
const (
	StmtStatusFullscanStep StmtStatus = sqlite3.SQLITE_STMTSTATUS_FULLSCAN_STEP
	StmtStatusSort         StmtStatus = sqlite3.SQLITE_STMTSTATUS_SORT
	StmtStatusAutoindex    StmtStatus = sqlite3.SQLITE_STMTSTATUS_AUTOINDEX
	StmtStatusVMStep       StmtStatus = sqlite3.SQLITE_STMTSTATUS_VM_STEP
	StmtStatusReprepare    StmtStatus = sqlite3.SQLITE_STMTSTATUS_REPREPARE
	StmtStatusRun          StmtStatus = sqlite3.SQLITE_STMTSTATUS_RUN
	StmtStatusFilterMiss   StmtStatus = sqlite3.SQLITE_STMTSTATUS_FILTER_MISS
	StmtStatusFilterHit    StmtStatus = sqlite3.SQLITE_STMTSTATUS_FILTER_HIT
	StmtStatusMemUsed      StmtStatus = sqlite3.SQLITE_STMTSTATUS_MEMUSED
)

// Status returns a per-statement counter (full-scan steps, sorts, autoindex
// rows, VM steps, …) from sqlite3_stmt_status. Pass reset=true to zero the
// counter after reading. Returns 0 for a finalized statement.
func (s *stmt) Status(op StmtStatus, reset bool) int {
	if s.pstmt == 0 {
		return 0
	}
	return int(sqlite3.Xsqlite3_stmt_status(s.c.tls, s.pstmt, int32(op), libc.Bool32(reset)))
}

// ExplainMode is a prepared statement's EXPLAIN mode, read or set via
// [Stmt.IsExplain] / [Stmt.Explain] (sqlite3_stmt_isexplain / _explain).
type ExplainMode int32

const (
	// ExplainOff runs the statement normally (produces its own results).
	ExplainOff ExplainMode = 0
	// ExplainFull turns the statement into EXPLAIN — stepping it yields the
	// virtual-machine bytecode program instead of the query's rows.
	ExplainFull ExplainMode = 1
	// ExplainQueryPlan turns the statement into EXPLAIN QUERY PLAN — stepping it
	// yields the high-level query plan.
	ExplainQueryPlan ExplainMode = 2
)

// IsExplain reports the statement's current EXPLAIN mode (sqlite3_stmt_isexplain).
func (s *stmt) IsExplain() ExplainMode {
	if s.pstmt == 0 {
		return ExplainOff
	}
	return ExplainMode(sqlite3.Xsqlite3_stmt_isexplain(s.c.tls, s.pstmt))
}

// Explain changes a prepared statement between normal, EXPLAIN, and EXPLAIN
// QUERY PLAN mode at runtime (sqlite3_stmt_explain), so the same statement can
// be inspected as a plan and then run — no re-preparing, and bound parameters
// carry over. It must be called when the statement is reset / not mid-run; SQLite
// returns an error otherwise. Requires SQLite 3.46 or newer.
func (s *stmt) Explain(mode ExplainMode) error {
	if s.pstmt == 0 {
		return errors.New("sqlite: Explain on a finalized statement")
	}
	if rc := sqlite3.Xsqlite3_stmt_explain(s.c.tls, s.pstmt, int32(mode)); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlite: stmt_explain: %w", s.c.errstr(rc))
	}
	return nil
}
