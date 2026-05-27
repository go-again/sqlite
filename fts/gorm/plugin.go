package ftsgorm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

const pluginName = "ftsgorm"

// plugin implements gorm.Plugin. Owns a registry of parsed tableMeta
// keyed on each model's reflect.Type so the row-level callbacks can
// re-use the parsed shape without walking tags every time.
type plugin struct {
	mu   sync.RWMutex
	meta map[reflect.Type]*modelMeta
}

type modelMeta struct {
	// SourceTable is the gorm-resolved table name (subject to gorm's
	// NamingStrategy).
	SourceTable string

	// PKField is the model's single primary key field. ftsgorm requires
	// a single integer PK so it can serve as the FTS5 rowid.
	PKField *schema.Field

	// SoftDelete is true when the model uses gorm.DeletedAt. Only
	// meaningful for ModeExternal — in-table and contentless modes
	// don't carry a deleted_at mirror because their source-of-truth
	// is the FTS5 table itself.
	SoftDelete bool

	// Table is the FTS5 table name.
	Table string

	// Tokenize, Prefix, Detail come from the merged tableMeta.
	Tokenize, Prefix, Detail string

	// Mode picks how the FTS5 table relates to the gorm source: external
	// (default), in-table, or contentless. See the Mode constants for
	// the tradeoffs.
	Mode Mode

	// Fields lists each tagged string column.
	Fields []fieldMeta
}

// Plugin returns a new gorm.Plugin instance for db.Use(...).
//
// Capabilities:
//
//   - AutoMigrate via ftsgorm.Migrate(db, models...) creates the source
//     tables, the shared FTS5 external-content table, and AFTER
//     INSERT/UPDATE/DELETE triggers.
//   - db.Create / db.Save / db.Delete on the source automatically
//     update the FTS5 index via the triggers (the plugin doesn't need
//     its own row callbacks for these — triggers fire reliably for
//     raw SQL writes too).
//   - Search[T](ctx, db, query, ...) returns matching gorm models in
//     rank order with optional snippet / highlight columns.
//   - DropSidecar(db, model) drops the FTS5 table + its triggers.
func Plugin() gorm.Plugin {
	return &plugin{meta: map[reflect.Type]*modelMeta{}}
}

func (*plugin) Name() string { return pluginName }

func (p *plugin) Initialize(db *gorm.DB) error {
	cb := db.Callback()
	if err := cb.Create().After("gorm:create").Register("ftsgorm:after_create", p.afterCreate); err != nil {
		return err
	}
	if err := cb.Update().After("gorm:update").Register("ftsgorm:after_update", p.afterUpdate); err != nil {
		return err
	}
	if err := cb.Delete().After("gorm:delete").Register("ftsgorm:after_delete", p.afterDelete); err != nil {
		return err
	}
	return nil
}

// registerSchema parses tags + caches the result. Re-parse is
// idempotent; concurrent access is safe under the rwmutex.
func (p *plugin) registerSchema(db *gorm.DB, model any) (*modelMeta, error) {
	rt := indirectType(reflect.TypeOf(model))
	p.mu.RLock()
	if mm, ok := p.meta[rt]; ok {
		p.mu.RUnlock()
		return mm, nil
	}
	p.mu.RUnlock()

	tm, err := parseTags(rt)
	if err != nil {
		return nil, err
	}
	if tm == nil {
		// Cache empty result.
		empty := &modelMeta{}
		p.mu.Lock()
		p.meta[rt] = empty
		p.mu.Unlock()
		return empty, nil
	}

	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return nil, fmt.Errorf("ftsgorm: parse %s: %w", rt.Name(), err)
	}
	pkFields := stmt.Schema.PrimaryFields
	if len(pkFields) != 1 {
		return nil, fmt.Errorf(
			"ftsgorm: %s has %d primary-key fields; FTS5 rowids require exactly one int PK",
			rt.Name(), len(pkFields))
	}

	mm := &modelMeta{
		SourceTable: stmt.Schema.Table,
		PKField:     pkFields[0],
		SoftDelete:  stmt.Schema.LookUpField("DeletedAt") != nil,
		Table:       tm.Table,
		Tokenize:    tm.Tokenize,
		Prefix:      tm.Prefix,
		Detail:      tm.Detail,
		Mode:        tm.Mode,
		Fields:      tm.Fields,
	}
	if mm.Table == "" {
		mm.Table = mm.SourceTable + "_fts"
	}

	p.mu.Lock()
	p.meta[rt] = mm
	p.mu.Unlock()
	return mm, nil
}

// lookupForDB returns the modelMeta for the current statement's model,
// or false if the model has no fts5: tags. Used by row-level callbacks
// to skip unrelated writes quickly.
func (p *plugin) lookupForDB(db *gorm.DB) (*modelMeta, bool) {
	if db.Statement == nil || db.Statement.Schema == nil {
		return nil, false
	}
	rt := db.Statement.Schema.ModelType
	p.mu.RLock()
	mm, ok := p.meta[rt]
	p.mu.RUnlock()
	if ok {
		return mm, len(mm.Fields) > 0
	}
	mm, err := p.registerSchema(db, db.Statement.Model)
	if err != nil {
		_ = db.AddError(err)
		return nil, false
	}
	return mm, len(mm.Fields) > 0
}

// ErrNotInstalled is returned by helper APIs when ftsgorm.Plugin() has
// not been registered on the *gorm.DB. Mirrors [vecgorm.ErrNotInstalled]
// so callers can match either bridge symmetrically via errors.Is.
var ErrNotInstalled = errors.New("ftsgorm: Plugin() not installed on *gorm.DB")

// pluginFrom returns the registered plugin instance or an error. The
// error wraps [ErrNotInstalled] when the plugin has not been installed.
func pluginFrom(db *gorm.DB) (*plugin, error) {
	raw, ok := db.Config.Plugins[pluginName]
	if !ok {
		return nil, fmt.Errorf("%w; call db.Use(ftsgorm.Plugin()) first", ErrNotInstalled)
	}
	p, ok := raw.(*plugin)
	if !ok {
		return nil, fmt.Errorf("ftsgorm: registered plugin %s is %T, not *ftsgorm.plugin", pluginName, raw)
	}
	return p, nil
}

func indirectType(t reflect.Type) reflect.Type {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			t = t.Elem()
		default:
			return t
		}
	}
}

func helperContext(db *gorm.DB) context.Context {
	if db.Statement != nil && db.Statement.Context != nil {
		return db.Statement.Context
	}
	return context.Background()
}

// execPool is the subset of gorm.ConnPool we need to issue FTS5
// statements. Both *sql.DB and *sql.Tx satisfy it; using this interface
// in callbacks and Search lets writes and reads participate in an
// active gorm.Transaction rather than auto-committing through the
// parent *sql.DB.
type execPool interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// activePool returns the connection pool the gorm.DB is currently using.
// Inside a gorm transaction (db.Transaction / db.Begin) this is the
// active *sql.Tx, so FTS5 writes commit or roll back with the parent.
// Outside a transaction it is the underlying *sql.DB.
func activePool(db *gorm.DB) (execPool, error) {
	if db.Statement != nil && db.Statement.ConnPool != nil {
		if p, ok := db.Statement.ConnPool.(execPool); ok {
			return p, nil
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("ftsgorm: unable to obtain ConnPool: %w", err)
	}
	return sqlDB, nil
}
