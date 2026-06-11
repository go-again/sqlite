package sqlite // import "github.com/go-again/sqlite"

import (
	"errors"
	"fmt"
	"math"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// This file exposes the SQLite SESSION extension (changesets / patchsets) as a
// typed Go API. A Session records every change made to attached tables on a
// connection; the recording can be serialized to a changeset or patchset blob,
// inverted, concatenated, and applied to another database with a Go conflict
// handler. No pure-Go SQLite driver exposed this before — the whole
// sqlite3session_* / sqlite3changeset_* family is compiled into the lib, and
// the apply callbacks are dispatched through trampolines + an id registry, the
// same shape as the rtree and scalar-UDF machinery.
//
// https://sqlite.org/sessionintro.html

// Session records changes to one or more tables on the connection it was
// created from. It is not safe for concurrent use; serialize access. Close it
// when done.
type Session struct {
	c        *conn
	pSession uintptr
}

// CreateSession starts a new change-recording session on the given schema
// (database) of the connection. Pass schema="" for "main". Attach tables with
// [Session.Attach] before making changes.
//
// https://sqlite.org/session/sqlite3session_create.html
func (c *Conn) CreateSession(schema string) (*Session, error) {
	if schema == "" {
		schema = "main"
	}
	zDb, err := libc.CString(schema)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(c.tls, zDb)

	bp := c.tls.Alloc(int(ptrSize))
	defer c.tls.Free(int(ptrSize))
	rc := sqlite3.Xsqlite3session_create(c.tls, c.db, zDb, bp)
	if rc != sqlite3.SQLITE_OK {
		return nil, fmt.Errorf("sqlite: CreateSession: %w", c.errstr(rc))
	}
	return &Session{c: c, pSession: *(*uintptr)(unsafe.Pointer(bp))}, nil
}

// Attach starts recording changes to a table. Pass table="" to record changes
// to every table that has a PRIMARY KEY. Call before the changes are made.
func (s *Session) Attach(table string) error {
	var zTab uintptr
	if table != "" {
		z, err := libc.CString(table)
		if err != nil {
			return err
		}
		defer libc.Xfree(s.c.tls, z)
		zTab = z
	}
	if rc := sqlite3.Xsqlite3session_attach(s.c.tls, s.pSession, zTab); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlite: Session.Attach(%q): %w", table, s.c.errstr(rc))
	}
	return nil
}

// Enable turns change recording on or off. Sessions start enabled; disabling
// lets a caller make changes that should not be captured.
func (s *Session) Enable(on bool) {
	sqlite3.Xsqlite3session_enable(s.c.tls, s.pSession, libc.Bool32(on))
}

// IsEnabled reports whether change recording is currently on.
func (s *Session) IsEnabled() bool {
	// A negative argument queries the state without changing it.
	return sqlite3.Xsqlite3session_enable(s.c.tls, s.pSession, -1) != 0
}

// IsEmpty reports whether the session has recorded no changes yet.
func (s *Session) IsEmpty() bool {
	return sqlite3.Xsqlite3session_isempty(s.c.tls, s.pSession) != 0
}

// Changeset serializes the recorded changes into a changeset blob, which
// records the full before-and-after of every change (so it can be inverted).
// Returns nil for an empty session.
func (s *Session) Changeset() ([]byte, error) {
	return s.extract(sqlite3.Xsqlite3session_changeset, "Changeset")
}

// Patchset serializes the recorded changes into a patchset blob — like a
// changeset but smaller, omitting the original values of unchanged columns and
// of DELETEs beyond the primary key. Patchsets cannot be inverted.
func (s *Session) Patchset() ([]byte, error) {
	return s.extract(sqlite3.Xsqlite3session_patchset, "Patchset")
}

// extract runs a (pSession, *pn, **pp) serializer through the shared
// changeset-buffer copy-out helper.
func (s *Session) extract(fn func(*libc.TLS, uintptr, uintptr, uintptr) int32, name string) ([]byte, error) {
	return s.c.transformChangeset("Session."+name, func(pn, pp uintptr) int32 {
		return fn(s.c.tls, s.pSession, pn, pp)
	})
}

// Diff records, into this session, the differences between the same-named
// table in fromSchema and the schema the session is recording. It is the
// "diff two databases" primitive: ATTACH another database, then Diff its table
// against this one to capture the changeset that would make them equal.
//
// https://sqlite.org/session/sqlite3session_diff.html
func (s *Session) Diff(fromSchema, table string) error {
	zFrom, err := libc.CString(fromSchema)
	if err != nil {
		return err
	}
	defer libc.Xfree(s.c.tls, zFrom)
	zTbl, err := libc.CString(table)
	if err != nil {
		return err
	}
	defer libc.Xfree(s.c.tls, zTbl)

	bp := s.c.tls.Alloc(int(ptrSize))
	defer s.c.tls.Free(int(ptrSize))
	*(*uintptr)(unsafe.Pointer(bp)) = 0
	rc := sqlite3.Xsqlite3session_diff(s.c.tls, s.pSession, zFrom, zTbl, bp)
	if rc != sqlite3.SQLITE_OK {
		if pErr := *(*uintptr)(unsafe.Pointer(bp)); pErr != 0 {
			msg := libc.GoString(pErr)
			sqlite3.Xsqlite3_free(s.c.tls, pErr)
			if msg != "" {
				return &Error{msg: "sqlite: Session.Diff: " + msg, code: int(rc)}
			}
		}
		return fmt.Errorf("sqlite: Session.Diff(%q, %q): %w", fromSchema, table, s.c.errstr(rc))
	}
	return nil
}

// Close releases the session. Subsequent calls are no-ops. Closing does not
// affect already-serialized changesets.
func (s *Session) Close() error {
	if s.pSession != 0 {
		sqlite3.Xsqlite3session_delete(s.c.tls, s.pSession)
		s.pSession = 0
	}
	return nil
}

// InvertChangeset returns the inverse of a changeset: applying the inverse
// undoes what the original applies (INSERT↔DELETE, UPDATE reversed). Patchsets
// cannot be inverted. The connection is used only for its allocator; the
// database is not touched.
func (c *Conn) InvertChangeset(changeset []byte) ([]byte, error) {
	in, n, free, err := c.cBytes(changeset)
	if err != nil {
		return nil, fmt.Errorf("sqlite: InvertChangeset: %w", err)
	}
	defer free()
	return c.transformChangeset("InvertChangeset", func(pn, pp uintptr) int32 {
		return sqlite3.Xsqlite3changeset_invert(c.tls, n, in, pn, pp)
	})
}

// ConcatChangesets returns a single changeset equivalent to applying a then b.
// The connection is used only for its allocator; the database is not touched.
func (c *Conn) ConcatChangesets(a, b []byte) ([]byte, error) {
	pa, na, freeA, err := c.cBytes(a)
	if err != nil {
		return nil, fmt.Errorf("sqlite: ConcatChangesets: %w", err)
	}
	defer freeA()
	pb, nb, freeB, err := c.cBytes(b)
	if err != nil {
		return nil, fmt.Errorf("sqlite: ConcatChangesets: %w", err)
	}
	defer freeB()
	return c.transformChangeset("ConcatChangesets", func(pn, pp uintptr) int32 {
		return sqlite3.Xsqlite3changeset_concat(c.tls, na, pa, nb, pb, pn, pp)
	})
}

// transformChangeset runs a changeset producer that writes (*pn int, **pp buf)
// and copies the SQLite-allocated output into a Go slice.
func (c *Conn) transformChangeset(name string, run func(pn, pp uintptr) int32) ([]byte, error) {
	bp := c.tls.Alloc(16)
	defer c.tls.Free(16)
	pp, pn := bp, bp+8
	if rc := run(pn, pp); rc != sqlite3.SQLITE_OK {
		return nil, fmt.Errorf("sqlite: %s: %w", name, c.errstr(rc))
	}
	n := *(*int32)(unsafe.Pointer(pn))
	p := *(*uintptr)(unsafe.Pointer(pp))
	if p == 0 || n <= 0 {
		return nil, nil
	}
	defer sqlite3.Xsqlite3_free(c.tls, p)
	out := make([]byte, n)
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(p)), n))
	return out, nil
}

// cBytes copies a Go slice into a C buffer for passing to a changeset C
// function. An empty slice yields (0, 0, no-op, nil). It errors when the slice
// exceeds the int32 length the C API uses — a >2GB blob would otherwise wrap
// negative and corrupt the changeset parser's bounds checks on untrusted input
// — and when allocation fails, so callers can't mistake a failed alloc for an
// empty changeset.
func (c *Conn) cBytes(b []byte) (ptr uintptr, n int32, free func(), err error) {
	noop := func() {}
	if len(b) == 0 {
		return 0, 0, noop, nil
	}
	if len(b) > math.MaxInt32 {
		return 0, 0, noop, fmt.Errorf("changeset too large: %d bytes exceeds int32", len(b))
	}
	p, merr := c.malloc(len(b))
	if merr != nil || p == 0 {
		return 0, 0, noop, errors.New("failed to allocate changeset buffer")
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(p)), len(b)), b)
	return p, int32(len(b)), func() { c.free(p) }, nil
}

// ConflictType classifies why applying a change conflicted with the target
// database, passed to a [ConflictHandler].
type ConflictType int32

const (
	// ConflictData: a DELETE or UPDATE found a row whose non-PK values differ
	// from the changeset's expected originals.
	ConflictData ConflictType = sqlite3.SQLITE_CHANGESET_DATA
	// ConflictNotFound: a DELETE or UPDATE found no row with the change's PK.
	ConflictNotFound ConflictType = sqlite3.SQLITE_CHANGESET_NOTFOUND
	// ConflictConflict: an INSERT found a row with a duplicate PK.
	ConflictConflict ConflictType = sqlite3.SQLITE_CHANGESET_CONFLICT
	// ConflictConstraint: applying the change violated a constraint
	// (UNIQUE/NOT NULL/CHECK).
	ConflictConstraint ConflictType = sqlite3.SQLITE_CHANGESET_CONSTRAINT
	// ConflictForeignKey: applying the changeset left foreign-key violations.
	ConflictForeignKey ConflictType = sqlite3.SQLITE_CHANGESET_FOREIGN_KEY
)

// String renders the conflict type for diagnostics.
func (t ConflictType) String() string {
	switch t {
	case ConflictData:
		return "data"
	case ConflictNotFound:
		return "notfound"
	case ConflictConflict:
		return "conflict"
	case ConflictConstraint:
		return "constraint"
	case ConflictForeignKey:
		return "foreignkey"
	default:
		return fmt.Sprintf("ConflictType(%d)", int32(t))
	}
}

// ConflictAction is what a [ConflictHandler] returns to resolve a conflict.
type ConflictAction int32

const (
	// ChangesetOmit skips the conflicting change and continues.
	ChangesetOmit ConflictAction = sqlite3.SQLITE_CHANGESET_OMIT
	// ChangesetReplace overwrites the conflicting row with the change. Only
	// valid for ConflictData and ConflictConflict; returning it for other
	// conflict types makes Apply fail with SQLITE_MISUSE.
	ChangesetReplace ConflictAction = sqlite3.SQLITE_CHANGESET_REPLACE
	// ChangesetAbort rolls back the whole apply and returns an error.
	ChangesetAbort ConflictAction = sqlite3.SQLITE_CHANGESET_ABORT
)

// ConflictHandler decides how to resolve a conflict encountered while applying
// a changeset. With no handler, conflicts abort the apply.
type ConflictHandler func(ConflictType) ConflictAction

// TableFilter decides whether to apply the changes recorded for a table
// (return true to apply). With no filter, every table is applied.
type TableFilter func(table string) bool

type applyConfig struct {
	conflict ConflictHandler
	filter   TableFilter
}

// ApplyOption configures [Conn.ApplyChangeset].
type ApplyOption func(*applyConfig)

// WithConflictHandler sets the conflict-resolution callback.
func WithConflictHandler(h ConflictHandler) ApplyOption {
	return func(c *applyConfig) { c.conflict = h }
}

// WithTableFilter restricts the apply to tables for which filter returns true.
func WithTableFilter(filter TableFilter) ApplyOption {
	return func(c *applyConfig) { c.filter = filter }
}

var applyReg = newCallbackTable[*applyConfig]()

// ApplyChangeset applies a changeset (or patchset) to this connection's
// database inside a savepoint. Conflicts are resolved by a [ConflictHandler]
// (WithConflictHandler); without one, the first conflict aborts and rolls
// back. WithTableFilter restricts which tables are touched.
//
// https://sqlite.org/session/sqlite3changeset_apply.html
func (c *Conn) ApplyChangeset(changeset []byte, opts ...ApplyOption) error {
	if len(changeset) == 0 {
		return nil
	}
	cfg := &applyConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	id := applyReg.register(cfg)
	defer applyReg.drop(id)

	pData, n, free, err := c.cBytes(changeset)
	if err != nil {
		return fmt.Errorf("sqlite: ApplyChangeset: %w", err)
	}
	defer free()

	rc := sqlite3.Xsqlite3changeset_apply_v2(c.tls, c.db, n, pData,
		cFuncPointer(applyFilterTrampoline), cFuncPointer(applyConflictTrampoline),
		id, 0, 0, 0)
	if rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlite: ApplyChangeset: %w", c.errstr(rc))
	}
	return nil
}

// applyFilterTrampoline is the C entry point for the apply table filter.
// Signature: int (*)(void *pCtx, const char *zTab).
func applyFilterTrampoline(tls *libc.TLS, pCtx uintptr, zTab uintptr) int32 {
	cfg, ok := applyReg.lookup(pCtx)
	if !ok || cfg.filter == nil {
		return 1 // apply every table
	}
	if cfg.filter(libc.GoString(zTab)) {
		return 1
	}
	return 0
}

// applyConflictTrampoline is the C entry point for the apply conflict handler.
// Signature: int (*)(void *pCtx, int eConflict, sqlite3_changeset_iter*).
func applyConflictTrampoline(tls *libc.TLS, pCtx uintptr, eConflict int32, _ uintptr) int32 {
	cfg, ok := applyReg.lookup(pCtx)
	if !ok || cfg.conflict == nil {
		return int32(ChangesetAbort)
	}
	return int32(cfg.conflict(ConflictType(eConflict)))
}

// compile-time guards that the trampolines match the apply_v2 callback types.
var (
	_ func(*libc.TLS, uintptr, uintptr) int32        = applyFilterTrampoline
	_ func(*libc.TLS, uintptr, int32, uintptr) int32 = applyConflictTrampoline
)
