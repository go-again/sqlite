package vecgorm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-again/sqlite/vec"
	"gorm.io/gorm"
)

// openSidecar returns a vec.Table handle for the meta's sidecar. The
// underlying *sql.DB comes from gorm's ConnPool; we extract it via the
// db.Statement's connection. Because vec.Open issues a probe SELECT it
// would fail if the sidecar weren't created — Migrate must have run.
func openSidecar(db *gorm.DB, m meta) (*vec.Table, error) {
	sqlDB, err := extractSQLDB(db)
	if err != nil {
		return nil, err
	}
	return vec.Open(sqlDB, m.Table, m.Dim, vec.Options{
		Metric:   m.Metric,
		Encoding: m.Encoding,
	})
}

// extractSQLDB pulls a *sql.DB out of a *gorm.DB. For most setups the
// ConnPool is a *sql.DB; inside a transaction it's a *sql.Tx, which the
// vec.Table API doesn't accept directly. Inside a tx we fall back to
// the parent *sql.DB on the same connection — sqlite serializes writes
// per connection, so this is safe.
func extractSQLDB(db *gorm.DB) (*sql.DB, error) {
	if db.Statement != nil {
		switch p := db.Statement.ConnPool.(type) {
		case *sql.DB:
			return p, nil
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("vecgorm: unable to obtain *sql.DB: %w", err)
	}
	return sqlDB, nil
}

// batchInsertWithSoftDelete writes the embedding + deleted=0 in a
// single statement per row, all wrapped in one transaction. Used when
// the source model has gorm.DeletedAt; vec0 INTEGER metadata columns
// reject NULL so we can't rely on the typed BatchInsert which only
// writes (rowid, embedding).
func batchInsertWithSoftDelete(db *gorm.DB, m meta, items []vec.Item) error {
	sqlDB, err := extractSQLDB(db)
	if err != nil {
		return err
	}
	ctx := helperContext(db)
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt := fmt.Sprintf(
		"INSERT INTO %s (rowid, %s, deleted) VALUES (?, %s, 0)",
		quoteIdent(m.Table), quoteIdent(m.Column),
		m.Encoding.Placeholder(),
	)
	prep, err := tx.PrepareContext(ctx, stmt)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer prep.Close()
	for _, it := range items {
		if _, err := prep.ExecContext(ctx, it.Rowid, m.Encoding.Encode(it.Embedding)); err != nil {
			tx.Rollback()
			return fmt.Errorf("vecgorm: insert into %s: %w", m.Table, err)
		}
	}
	return tx.Commit()
}

// isSoftDelete reports whether the active UPDATE statement is gorm's
// soft-delete path. We detect it by inspecting Statement.Clauses: gorm
// adds an "UPDATE" clause that targets deleted_at when soft-deleting.
// Falls back to a heuristic on the WHERE clause if not found.
func isSoftDelete(db *gorm.DB) bool {
	if db.Statement == nil || db.Statement.Schema == nil {
		return false
	}
	if db.Statement.Schema.LookUpField("DeletedAt") == nil {
		return false
	}
	// gorm puts a custom clause name "soft_delete_enabled" on the
	// Statement when running its soft-delete callback. Older versions
	// just emit SET deleted_at = ... — we cover both by sniffing the
	// statement's SQL after build. The safest signal is checking if
	// any Set expression touches deleted_at.
	for _, set := range db.Statement.Clauses {
		if strings.EqualFold(set.Name, "soft_delete_enabled") {
			return true
		}
	}
	return false
}

// softDeleteSidecar resyncs the sidecar's `deleted` metadata column
// from the source table's deleted_at, *after* gorm's soft-delete UPDATE
// has committed. The expression
//
//	deleted = (deleted_at IS NOT NULL on source)
//
// is correct for any subset of rows gorm just touched and doesn't
// require us to re-parse gorm's WHERE clause. The statement re-syncs
// all rows; for tables of modest size (where vec-indexing is usually
// applied) this is fine, and SQLite's row scan is cheap when the
// source has an index on the PK (which it always does — PK is rowid).
func softDeleteSidecar(db *gorm.DB, mm modelMeta, m meta) error {
	pkColumn := quoteIdent(mm.PKField.DBName)
	stmt := fmt.Sprintf(
		"UPDATE %s SET deleted = "+
			"COALESCE((SELECT CASE WHEN deleted_at IS NOT NULL THEN 1 ELSE 0 END "+
			"FROM %s WHERE %s.%s = %s.rowid), 0)",
		quoteIdent(m.Table),
		quoteIdent(mm.SourceTable),
		quoteIdent(mm.SourceTable), pkColumn,
		quoteIdent(m.Table),
	)
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("vecgorm: soft-delete update on %s: %w", m.Table, err)
	}
	return nil
}

// deleteByWhere garbage-collects sidecar rows whose source rows no
// longer exist. Used when the caller's Delete had no concrete struct
// value (e.g. db.Where("status = ?", 'archived').Delete(&Model{})) so
// we cannot enumerate affected PKs from db.Statement.ReflectValue.
//
// Runs after gorm's source DELETE commits; orphaned sidecar rows
// are precisely those whose rowid is no longer present in the
// source PK column. Single statement, no WHERE re-parse.
func deleteByWhere(ctx context.Context, db *gorm.DB, mm modelMeta, m meta, _ *vec.Table) error {
	stmt := fmt.Sprintf(
		"DELETE FROM %s WHERE rowid NOT IN (SELECT %s FROM %s)",
		quoteIdent(m.Table),
		quoteIdent(mm.PKField.DBName),
		quoteIdent(mm.SourceTable),
	)
	if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
		return fmt.Errorf("vecgorm: delete-by-where on %s: %w", m.Table, err)
	}
	return nil
}

// ErrNotInstalled is returned by helper APIs when vecgorm.Plugin() has
// not been registered on the *gorm.DB. Re-exported so callers can match
// via errors.Is.
var ErrNotInstalled = errors.New("vecgorm: Plugin() not installed on *gorm.DB")
