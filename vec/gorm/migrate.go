package vecgorm

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-again/sqlite/vec"
	"gorm.io/gorm"
)

// Migrate is the entry point that replaces db.AutoMigrate when using
// vecgorm. It:
//
//  1. Ensures every model has its tags parsed and its tagged fields
//     marked IgnoreMigration so gorm doesn't try to create columns
//     for the []float32 embedding values.
//  2. Calls db.AutoMigrate for the source tables.
//  3. Creates each sidecar vec0 virtual table (CREATE VIRTUAL TABLE
//     name USING vec0(rowid_column kind, embedding float[N] ...)).
//
// Repeat calls are idempotent: existing sidecars are left alone, and
// dim-mismatches between the tag and the existing sidecar are logged
// to db.Logger rather than silently corrected: a dim change means the
// stored vectors no longer match the model, and migrating that
// silently would erase the operator's chance to spot the schema
// drift before queries start failing.
func Migrate(db *gorm.DB, models ...any) error {
	p, err := pluginFrom(db)
	if err != nil {
		return err
	}

	metas := make([]modelMeta, 0, len(models))
	for _, m := range models {
		mm, err := p.registerSchema(db, m)
		if err != nil {
			return err
		}
		metas = append(metas, mm)
	}

	// Source tables first, then sidecars. AutoMigrate may add columns
	// to the source the sidecar references (e.g. soft-delete deleted_at);
	// running it first means the schema is stable before we read it.
	if err := db.AutoMigrate(models...); err != nil {
		return err
	}

	ctx := db.Statement.Context
	if ctx == nil {
		ctx = helperContext(db)
	}

	for i, mm := range metas {
		for _, m := range mm.Fields {
			if err := ensureSidecar(ctx, db, models[i], mm, m); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureSidecar creates the vec0 sidecar table if it doesn't exist, or
// warns if the existing one has a different dim than the tag declares.
//
// Sidecar schema includes the embedding column and, when the source
// model uses gorm.DeletedAt, an additional `deleted INTEGER` metadata
// column that the callbacks set to 0/1. KNN uses WithFilter on this
// column to exclude soft-deleted rows by default.
func ensureSidecar(ctx context.Context, db *gorm.DB, _ any, _ modelMeta, m meta) error {
	_ = ctx // reserved for future use; current statements run through gorm.
	// vec0 doesn't accept quoted identifiers in its constructor; we
	// validated the table/column names via isIdent at tag-parse time.
	cols := []string{
		fmt.Sprintf("%s float[%d] distance_metric=%s", m.Column, m.Dim, m.Metric.Keyword()),
	}
	if m.SoftDelete {
		cols = append(cols, "deleted integer")
	}

	stmt := fmt.Sprintf(
		"CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(%s)",
		m.Table, strings.Join(cols, ", "),
	)
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("vecgorm: create sidecar %s: %w", m.Table, err)
	}

	// Dim-mismatch detection: PRAGMA table_info or a probe SELECT both
	// fail on vec0 (virtual tables). The cleanest probe is to issue a
	// dim-mismatch INSERT and see if it errors with the documented
	// "expected ..." message — but that's noisy. We instead use the
	// vec0-internal _info table that sqlite-vec exposes; if a future
	// version drops that, this falls back to a no-op warning path.
	if err := checkDimMismatch(db, m); err != nil {
		db.Logger.Warn(db.Statement.Context, "%v", err)
	}
	return nil
}

// checkDimMismatch reads the sidecar's stored dim and warns if it
// disagrees with the tag. Returns nil + warning-printed via the gorm
// logger; intentionally non-fatal so users can roll forward by manually
// dropping the sidecar.
func checkDimMismatch(db *gorm.DB, m meta) error {
	// sqlite-vec maintains an internal "<name>_info" shadow table on
	// every vec0 virtual table. We query it by name; if the schema
	// changes upstream this becomes a no-op (the warning silently goes
	// away rather than firing on a falsely-detected mismatch).
	shadow := m.Table + "_info"
	var key, val string
	row := db.Raw(fmt.Sprintf(
		"SELECT key, value FROM %s WHERE key = 'CREATE_VTAB'",
		quoteIdent(shadow),
	)).Row()
	if err := row.Scan(&key, &val); err != nil {
		return nil // shadow table moved or never existed; nothing to compare against.
	}
	// CREATE_VTAB contains the original CREATE statement; we look for
	// "float[N]" with N matching our dim.
	needle := fmt.Sprintf("float[%d]", m.Dim)
	if strings.Contains(strings.ToLower(val), strings.ToLower(needle)) {
		return nil
	}
	return fmt.Errorf(
		"vecgorm: %s declared dim=%d but existing sidecar uses a different dimension; "+
			"drop %s manually to re-create",
		m.FieldName, m.Dim, m.Table,
	)
}

// DropSidecar drops the sidecar virtual table(s) for the given model.
// Use in test cleanup or when removing a model permanently. The
// gorm.io/go-again Migrator's DropTable path already invokes this
// via DropTableHook below, so users calling `db.Migrator().DropTable(&Model{})`
// don't need to call DropSidecar separately.
func DropSidecar(db *gorm.DB, model any) error {
	p, err := pluginFrom(db)
	if err != nil {
		return err
	}
	return p.dropSidecar(db, model)
}

// DropTableHook implements the sqlite-go-again gorm dialector's hook
// interface so `db.Migrator().DropTable(&Model{})` automatically
// cascades into the vec0 sidecar. The dialector iterates plugins and
// calls this method before issuing the source-table DROP.
//
// Method is defined on *plugin so the gorm Plugin instance (which is
// what gets stored in db.Config.Plugins) satisfies the interface.
func (p *plugin) DropTableHook(db *gorm.DB, model any) error {
	return p.dropSidecar(db, model)
}

// dropSidecar is the shared implementation behind DropSidecar (the
// package-level helper) and DropTableHook (the gorm dialector hook).
// Idempotent: each sidecar drop is `DROP TABLE IF EXISTS`.
func (p *plugin) dropSidecar(db *gorm.DB, model any) error {
	mm, err := p.registerSchema(db, model)
	if err != nil {
		return err
	}
	for _, m := range mm.Fields {
		if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", quoteIdent(m.Table))).Error; err != nil {
			return err
		}
	}
	return nil
}

// quoteIdent is a thin alias for vec.QuoteIdent kept so the local call
// sites read naturally; the real implementation lives in vec.
func quoteIdent(name string) string { return vec.QuoteIdent(name) }
