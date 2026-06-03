package sqlite

import (
	"database/sql/driver"
	"fmt"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
	"modernc.org/sqlite/vtab"
)

// Value is the value type ferried between SQLite and Go-implemented
// virtual table cursors (and the [Conn.RegisterFunc] surface). It aliases
// [database/sql/driver.Value] so consumers don't need a separate import.
type Value = driver.Value

// VTab is the interface a Go virtual table must implement. The minimum set
// is BestIndex + Open + Disconnect + Destroy; modules that also need writes
// implement [VTabUpdater], xRename implements [VTabRenamer], and per-table
// transaction state implements [VTabTransactional].
//
// Implementations declare their schema by calling [Conn.DeclareVTab] from
// inside the [VTabCtor].
type VTab = vtab.Table

// VTabCursor walks the rows a Filter call selected. EOF reports whether the
// cursor is past the last row (note: lowercase Eof per modernc upstream).
type VTabCursor = vtab.Cursor

// VTabUpdater is the optional interface a [VTab] implementation can satisfy
// to support INSERT / UPDATE / DELETE through xUpdate. Modules that do not
// implement it are read-only; SQLite returns SQLITE_READONLY on writes.
type VTabUpdater = vtab.Updater

// VTabRenamer is optional; modules that implement it handle xRename.
type VTabRenamer = vtab.Renamer

// VTabTransactional is optional; modules with per-table transaction state
// implement it to receive Begin / Sync / Commit / Rollback callbacks.
type VTabTransactional = vtab.Transactional

// IndexInfo, Constraint, OrderBy, ConstraintOp are re-exported so a [VTab]
// implementation of BestIndex doesn't need a second import.
type (
	IndexInfo    = vtab.IndexInfo
	Constraint   = vtab.Constraint
	OrderBy      = vtab.OrderBy
	ConstraintOp = vtab.ConstraintOp
)

// Constraint operator re-exports for BestIndex.
const (
	OpUnknown   = vtab.OpUnknown
	OpEQ        = vtab.OpEQ
	OpGT        = vtab.OpGT
	OpLE        = vtab.OpLE
	OpLT        = vtab.OpLT
	OpGE        = vtab.OpGE
	OpMATCH     = vtab.OpMATCH
	OpNE        = vtab.OpNE
	OpIS        = vtab.OpIS
	OpISNOT     = vtab.OpISNOT
	OpISNULL    = vtab.OpISNULL
	OpISNOTNULL = vtab.OpISNOTNULL
	OpLIKE      = vtab.OpLIKE
	OpGLOB      = vtab.OpGLOB
	OpREGEXP    = vtab.OpREGEXP
	OpFUNCTION  = vtab.OpFUNCTION
	OpLIMIT     = vtab.OpLIMIT
	OpOFFSET    = vtab.OpOFFSET
)

// VTabCtor builds a [VTab] from CREATE VIRTUAL TABLE arguments. The first
// three positional values are SQLite's module name, database name (typically
// "main"), and table name; args are the user's USING name(args...) arguments.
//
// The constructor MUST call [Conn.DeclareVTab] with the CREATE TABLE statement
// that describes the table's columns before returning, or SQLite will reject
// the CREATE.
type VTabCtor func(c *Conn, module, db, table string, args []string) (VTab, error)

// CreateModule registers a Go-implemented virtual table module on this
// connection under name. ctor builds a [VTab] on each CREATE VIRTUAL TABLE
// (xCreate) and on subsequent opens of an existing virtual table (xConnect);
// callers that need to distinguish the two cases can switch on the table
// argument or stash state in package-level maps.
//
// Registration is per-connection. For pool-wide application, install via a
// [Driver.ConnectHook] — the `ext/<name>/auto/` blank-import sub-packages
// do exactly that.
//
// Optional interfaces ([VTabUpdater], [VTabRenamer], [VTabTransactional]) are
// recognized automatically when the returned [VTab] implements them.
func (c *Conn) CreateModule(name string, ctor VTabCtor) error {
	if ctor == nil {
		return fmt.Errorf("sqlite: CreateModule %q: ctor is nil", name)
	}
	if name == "" {
		return fmt.Errorf("sqlite: CreateModule: name is empty")
	}
	return c.registerSingleModule(name, &vtabCtorModule{conn: c, createCtor: ctor, connectCtor: ctor})
}

// CreateEponymousModule registers a Go-implemented eponymous-only virtual
// table module: SQLite forbids CREATE VIRTUAL TABLE for the name, and
// instead instantiates the table via xConnect when the module name appears
// in a SELECT (e.g. `SELECT … FROM array(?)`). See
// https://sqlite.org/vtab.html#eponymous_virtual_tables for the contract.
//
// Use this for table-valued functions whose schema is fully determined by
// the constructor — there's nothing per-instance to persist via CREATE
// VIRTUAL TABLE. ext/array is the canonical example.
//
// Registration is per-connection; for pool-wide application use a
// [Driver.ConnectHook].
func (c *Conn) CreateEponymousModule(name string, ctor VTabCtor) error {
	if ctor == nil {
		return fmt.Errorf("sqlite: CreateEponymousModule %q: ctor is nil", name)
	}
	if name == "" {
		return fmt.Errorf("sqlite: CreateEponymousModule: name is empty")
	}
	return c.registerSingleEponymousModule(name, &vtabCtorModule{conn: c, createCtor: ctor, connectCtor: ctor})
}

// DeclareVTab calls sqlite3_declare_vtab to describe a virtual table's
// columns to SQLite. Only callable from inside a [VTabCtor]; calling it
// elsewhere returns SQLITE_MISUSE wrapped in a Go error.
func (c *Conn) DeclareVTab(schema string) error {
	zSchema, err := libc.CString(schema)
	if err != nil {
		return err
	}
	defer libc.Xfree(c.tls, zSchema)
	if rc := sqlite3.Xsqlite3_declare_vtab(c.tls, c.db, zSchema); rc != sqlite3.SQLITE_OK {
		return fmt.Errorf("sqlite: declare_vtab: %w", c.errstr(rc))
	}
	return nil
}

// vtabCtorModule adapts one [VTabCtor] (used for both xCreate and
// xConnect) or a pair of ctors (when distinct create/connect logic is
// required) into the lower-level [vtab.Module] interface that the
// existing trampolines (vtab.go) expect.
type vtabCtorModule struct {
	conn        *Conn
	createCtor  VTabCtor // called by xCreate (fresh CREATE VIRTUAL TABLE)
	connectCtor VTabCtor // called by xConnect (reopen of an existing vtab)
}

func (m *vtabCtorModule) Create(_ vtab.Context, args []string) (vtab.Table, error) {
	return m.invoke(m.createCtor, args)
}

func (m *vtabCtorModule) Connect(_ vtab.Context, args []string) (vtab.Table, error) {
	return m.invoke(m.connectCtor, args)
}

func (m *vtabCtorModule) invoke(ctor VTabCtor, args []string) (vtab.Table, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("sqlite: vtab ctor called with fewer than 3 args (got %d)", len(args))
	}
	return ctor(m.conn, args[0], args[1], args[2], args[3:])
}

// CreateModuleSplit registers a Go-implemented virtual table module on
// this connection under name, using separate create and connect ctors
// so the module can distinguish "first-time CREATE VIRTUAL TABLE" from
// "subsequent reopen of an existing vtab via the schema cache".
//
// Use this when the module persists state across sessions (e.g. a
// shadow storage table) and needs different logic for the first
// CREATE vs every later open. See [ext/bloom] and [ext/spellfix1] for
// canonical examples — both maintain a `<vtab>_storage` shadow table
// that is created on xCreate and merely opened on xConnect.
//
// Modules whose create and connect logic are identical should use the
// simpler [Conn.CreateModule] instead. Eponymous tables (table-valued
// functions) only ever go through xConnect; use
// [Conn.CreateEponymousModule] for those.
func (c *Conn) CreateModuleSplit(name string, create, connect VTabCtor) error {
	if create == nil {
		return fmt.Errorf("sqlite: CreateModuleSplit %q: create ctor is nil", name)
	}
	if connect == nil {
		return fmt.Errorf("sqlite: CreateModuleSplit %q: connect ctor is nil", name)
	}
	if name == "" {
		return fmt.Errorf("sqlite: CreateModuleSplit: name is empty")
	}
	return c.registerSingleModule(name, &vtabCtorModule{conn: c, createCtor: create, connectCtor: connect})
}
