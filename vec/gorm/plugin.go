package vecgorm

import (
	"fmt"
	"reflect"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/go-again/sqlite/internal/gormbridge"
)

// pluginName matches gorm's Plugin contract (unique per *gorm.DB).
const pluginName = "vecgorm"

// plugin is the gorm.Plugin implementation. It owns a small registry of
// parsed metadata keyed on the schema's reflect.Type so subsequent
// callbacks can find the meta without re-parsing tags.
type plugin struct {
	mu   sync.RWMutex
	meta map[reflect.Type]modelMeta
}

// modelMeta groups every vec-tagged field on one gorm model.
type modelMeta struct {
	// SourceTable is the gorm table name as resolved by gorm's
	// NamingStrategy. Captured at registration so callbacks don't have
	// to re-derive it.
	SourceTable string

	// PKField points at the gorm-parsed primary-key field; we read its
	// value at callback time to feed the sidecar's rowid.
	PKField *schema.Field

	// SoftDeleteColumn is the DBName of the model's gorm.DeletedAt
	// field, captured at schema-parse time so soft-delete sync uses the
	// model's actual column name rather than the hard-coded
	// "deleted_at" default. Empty when the model has no DeletedAt.
	SoftDeleteColumn string

	// Fields is the list of vec-tagged fields. One model can have
	// multiple embeddings (one sidecar per field).
	Fields []meta
}

// Plugin returns a new plugin instance suitable for db.Use(...).
//
// The returned plugin handles the full sidecar lifecycle:
//
//	db.Use(vecgorm.Plugin())
//	vecgorm.Migrate(db, &Document{}) // create source + sidecar
//	db.Create(&doc)                  // populate sidecar via AfterCreate
//	db.Save(&doc)                    // sync sidecar via AfterUpdate
//	db.Delete(&doc)                  // remove from sidecar via AfterDelete
//	db.Migrator().DropTable(&Document{}) // drop sidecar too
//
// Tagged []float32 fields are automatically excluded from gorm's own
// migration: we set field.IgnoreMigration = true at registration time
// so AutoMigrate (or vecgorm.Migrate) does not try to create a column
// for the embedding on the source table.
func Plugin() gorm.Plugin {
	return &plugin{meta: map[reflect.Type]modelMeta{}}
}

// Name returns the plugin's unique gorm name.
func (*plugin) Name() string { return pluginName }

// Initialize is invoked by gorm when the plugin is registered via
// db.Use. We register the row-level callbacks here; per-model schema
// rewriting happens lazily on first sight in registerSchema (called
// from Migrate or from a callback that runs before AutoMigrate).
func (p *plugin) Initialize(db *gorm.DB) error {
	cb := db.Callback()

	// All callbacks attach our hook AFTER gorm's own row write so we
	// can read the just-assigned PK from db.Statement.
	if err := cb.Create().After("gorm:create").Register("vecgorm:after_create", p.afterCreate); err != nil {
		return err
	}
	if err := cb.Update().After("gorm:update").Register("vecgorm:after_update", p.afterUpdate); err != nil {
		return err
	}
	if err := cb.Delete().After("gorm:delete").Register("vecgorm:after_delete", p.afterDelete); err != nil {
		return err
	}
	return nil
}

// registerSchema parses a model's struct tags into modelMeta and caches
// the result. Re-parsing the same type is idempotent. Safe to call
// concurrently. Side effect: flips IgnoreMigration / Creatable /
// Updatable / Readable to false on every vec-tagged field so gorm's
// own migrator and SQL builder ignore them.
//
// Pre-flight: any field with a vec:"..." tag MUST also have gorm:"-"
// because gorm's schema.Parse fails on unrecognized data types
// (it can't infer a SQL type for []float32). The pre-flight check
// runs before Parse and emits a clear error pointing at the offending
// field, rather than letting gorm's "unsupported data type: &[]"
// surface to the user.
func (p *plugin) registerSchema(db *gorm.DB, model any) (modelMeta, error) {
	rt := gormbridge.IndirectType(reflect.TypeOf(model))
	p.mu.RLock()
	if mm, ok := p.meta[rt]; ok {
		p.mu.RUnlock()
		return mm, nil
	}
	p.mu.RUnlock()

	if err := preflightTags(rt); err != nil {
		return modelMeta{}, err
	}

	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return modelMeta{}, fmt.Errorf("vecgorm: parse %s: %w", rt.Name(), err)
	}

	mm := modelMeta{SourceTable: stmt.Schema.Table}
	pkFields := stmt.Schema.PrimaryFields
	if len(pkFields) != 1 {
		return modelMeta{}, fmt.Errorf(
			"vecgorm: %s has %d primary-key fields; vec0 rowids require exactly one int PK",
			rt.Name(), len(pkFields))
	}
	mm.PKField = pkFields[0]
	if del := gormbridge.FindDeletedAtField(stmt.Schema); del != nil {
		mm.SoftDeleteColumn = del.DBName
	}

	for _, f := range stmt.Schema.Fields {
		tag, ok := f.StructField.Tag.Lookup(tagName)
		if !ok {
			continue
		}
		m, err := parseTag(tag, f.Name, f.StructField.Index)
		if err != nil {
			return modelMeta{}, err
		}
		// Fill in defaults that need schema context. The defaulted
		// `<source>_vec` name still has to pass isIdent — a gorm
		// model whose TableName() returns whitespace or a `;` would
		// otherwise land raw inside CREATE VIRTUAL TABLE. The explicit
		// `table=` tag value is already validated in parseTag.
		if m.Table == "" {
			m.Table = mm.SourceTable + "_vec"
		}
		if !isIdent(m.Table) {
			return modelMeta{}, fmt.Errorf(
				"vecgorm: invalid sidecar table name %q (derived from source table %q); set vec:\"…;table=<name>\" explicitly",
				m.Table, mm.SourceTable)
		}
		m.SoftDelete = gormbridge.FindDeletedAtField(stmt.Schema) != nil

		// Mute gorm's own SQL machinery for this field. The plugin
		// owns its persistence.
		f.IgnoreMigration = true
		f.Creatable = false
		f.Updatable = false
		f.Readable = false

		mm.Fields = append(mm.Fields, m)
	}

	// Double-checked locking: another goroutine may have cached an
	// equivalent meta while we were parsing. Re-check under the write
	// lock and prefer the cached entry so callers see one canonical
	// value. Without this, two concurrent first-access calls both pay
	// the schema.Parse cost.
	p.mu.Lock()
	if cached, ok := p.meta[rt]; ok {
		p.mu.Unlock()
		return cached, nil
	}
	p.meta[rt] = mm
	p.mu.Unlock()
	return mm, nil
}

// lookupForDB returns the modelMeta for the current statement's model,
// or false if the model has no vec tags. Used by row-level callbacks.
func (p *plugin) lookupForDB(db *gorm.DB) (modelMeta, bool) {
	if db.Statement == nil || db.Statement.Schema == nil {
		return modelMeta{}, false
	}
	rt := db.Statement.Schema.ModelType
	p.mu.RLock()
	mm, ok := p.meta[rt]
	p.mu.RUnlock()
	if ok {
		return mm, len(mm.Fields) > 0
	}
	// Lazy-parse on first row write so users can skip explicit
	// Migrate when not creating tables.
	mm, err := p.registerSchema(db, db.Statement.Model)
	if err != nil {
		_ = db.AddError(err)
		return modelMeta{}, false
	}
	return mm, len(mm.Fields) > 0
}

// pluginFrom extracts the registered plugin instance from a *gorm.DB.
// Returns an error wrapping [ErrNotInstalled] when Plugin() has not
// been installed via db.Use.
func pluginFrom(db *gorm.DB) (*plugin, error) {
	raw, ok := db.Config.Plugins[pluginName]
	if !ok {
		return nil, fmt.Errorf("%w; call db.Use(vecgorm.Plugin()) first", ErrNotInstalled)
	}
	p, ok := raw.(*plugin)
	if !ok {
		return nil, fmt.Errorf("vecgorm: registered plugin %s is %T, not *vecgorm.plugin", pluginName, raw)
	}
	return p, nil
}
