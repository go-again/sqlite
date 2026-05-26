package ftsgorm

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/go-again/sqlite/fts"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// afterCreate runs after a source-table INSERT. In ModeExternal the
// FTS5 triggers handle the sync, so this callback is a no-op. In
// ModeInTable / ModeContentless the plugin owns the sync — we INSERT
// into the FTS5 table directly using rowids gorm just assigned.
func (p *plugin) afterCreate(db *gorm.DB) {
	mm, ok := p.lookupForDB(db)
	if !ok || mm.Mode == ModeExternal {
		return
	}
	rows := iterateRows(db.Statement.ReflectValue)
	if len(rows) == 0 {
		return
	}
	if err := syncInsert(db, mm, rows); err != nil {
		_ = db.AddError(err)
	}
}

// afterUpdate handles source UPDATEs in non-external mode. For external
// mode the trigger handles it; in-table / contentless need explicit
// 'delete' + INSERT in FTS5.
func (p *plugin) afterUpdate(db *gorm.DB) {
	mm, ok := p.lookupForDB(db)
	if !ok || mm.Mode == ModeExternal {
		return
	}
	rows := iterateRows(db.Statement.ReflectValue)
	if len(rows) == 0 {
		return
	}
	if err := syncUpdate(db, mm, rows); err != nil {
		_ = db.AddError(err)
	}
}

// afterDelete handles source DELETEs in non-external mode. For external
// mode the trigger handles it.
func (p *plugin) afterDelete(db *gorm.DB) {
	mm, ok := p.lookupForDB(db)
	if !ok || mm.Mode == ModeExternal {
		return
	}
	rows := iterateRows(db.Statement.ReflectValue)
	if err := syncDelete(db, mm, rows); err != nil {
		_ = db.AddError(err)
	}
}

// syncInsert writes a row's text columns into the FTS5 table. We use
// the underlying *sql.DB directly rather than gorm's Exec because
// gorm's logger interpolates positional args in a way that confuses
// SQLite's bind for raw INSERTs into virtual tables — bypassing it
// keeps the args in declaration order.
func syncInsert(db *gorm.DB, mm *modelMeta, rows []reflect.Value) error {
	sqlDB, err := extractSQLDB(db)
	if err != nil {
		return err
	}
	ctx := helperContext(db)
	for _, row := range rows {
		rowid, ok := pkAsInt64(mm.PKField, row)
		if !ok {
			continue
		}
		vals, err := columnValues(mm, row)
		if err != nil {
			return err
		}
		placeholders := strings.Repeat("?,", len(mm.Fields))
		placeholders = strings.TrimSuffix(placeholders, ",")
		colNames := make([]string, len(mm.Fields))
		for i, f := range mm.Fields {
			colNames[i] = quoteIdent(f.Column)
		}
		stmt := fmt.Sprintf(
			"INSERT INTO %s(rowid, %s) VALUES (?, %s)",
			quoteIdent(mm.Table),
			strings.Join(colNames, ", "),
			placeholders,
		)
		if mm.SoftDelete {
			stmt = fmt.Sprintf(
				"INSERT INTO %s(rowid, %s, deleted) VALUES (?, %s, 0)",
				quoteIdent(mm.Table),
				strings.Join(colNames, ", "),
				placeholders,
			)
		}
		args := append([]any{rowid}, vals...)
		if _, err := sqlDB.ExecContext(ctx, stmt, args...); err != nil {
			return fmt.Errorf("ftsgorm: insert into %s: %w", mm.Table, err)
		}
	}
	return nil
}

// syncUpdate refreshes a row's text in the FTS5 table. We use FTS5's
// 'delete' + INSERT idiom which works for all non-external modes.
func syncUpdate(db *gorm.DB, mm *modelMeta, rows []reflect.Value) error {
	sqlDB, err := extractSQLDB(db)
	if err != nil {
		return err
	}
	ctx := helperContext(db)
	for _, row := range rows {
		rowid, ok := pkAsInt64(mm.PKField, row)
		if !ok {
			continue
		}
		if _, err := sqlDB.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE rowid = ?", quoteIdent(mm.Table)),
			rowid,
		); err != nil {
			return fmt.Errorf("ftsgorm: replace-delete in %s: %w", mm.Table, err)
		}
		if err := syncInsert(db, mm, []reflect.Value{row}); err != nil {
			return err
		}
		if mm.SoftDelete && isSoftDeleted(mm, row) {
			if _, err := sqlDB.ExecContext(ctx,
				fmt.Sprintf("UPDATE %s SET deleted = 1 WHERE rowid = ?", quoteIdent(mm.Table)),
				rowid,
			); err != nil {
				return fmt.Errorf("ftsgorm: flip deleted=1 in %s: %w", mm.Table, err)
			}
		}
	}
	return nil
}

// syncDelete removes rows from the FTS5 table on hard delete; soft
// delete is handled here too because gorm fires the Delete callback
// for both paths (soft-delete's underlying SQL is an UPDATE, but the
// callback chain is gorm:delete).
func syncDelete(db *gorm.DB, mm *modelMeta, rows []reflect.Value) error {
	sqlDB, err := extractSQLDB(db)
	if err != nil {
		return err
	}
	ctx := helperContext(db)
	if mm.SoftDelete {
		for _, row := range rows {
			rowid, ok := pkAsInt64(mm.PKField, row)
			if !ok {
				continue
			}
			if _, err := sqlDB.ExecContext(ctx,
				fmt.Sprintf("UPDATE %s SET deleted = 1 WHERE rowid = ?", quoteIdent(mm.Table)),
				rowid,
			); err != nil {
				return fmt.Errorf("ftsgorm: soft-delete in %s: %w", mm.Table, err)
			}
		}
		return nil
	}
	for _, row := range rows {
		rowid, ok := pkAsInt64(mm.PKField, row)
		if !ok {
			continue
		}
		if _, err := sqlDB.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE rowid = ?", quoteIdent(mm.Table)),
			rowid,
		); err != nil {
			return fmt.Errorf("ftsgorm: delete from %s: %w", mm.Table, err)
		}
	}
	return nil
}

// columnValues extracts the tagged text values from a row.
func columnValues(mm *modelMeta, row reflect.Value) ([]any, error) {
	out := make([]any, len(mm.Fields))
	for i, f := range mm.Fields {
		v := row.FieldByIndex(f.FieldIndex)
		for v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return nil, fmt.Errorf("ftsgorm: %s field is nil pointer", f.FieldName)
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.String {
			return nil, fmt.Errorf("ftsgorm: %s field is %s (expected string)", f.FieldName, v.Kind())
		}
		out[i] = v.String()
	}
	return out, nil
}

// isSoftDeleted reports whether a row's gorm.DeletedAt field is set.
func isSoftDeleted(_ *modelMeta, row reflect.Value) bool {
	rt := row.Type()
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Name == "DeletedAt" {
			f := row.Field(i)
			// gorm.DeletedAt is a struct with .Valid bool field.
			valid := f.FieldByName("Valid")
			if valid.IsValid() && valid.Bool() {
				return true
			}
		}
	}
	return false
}

// iterateRows normalizes db.Statement.ReflectValue into a flat list.
func iterateRows(v reflect.Value) []reflect.Value {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
		return []reflect.Value{v}
	case reflect.Slice, reflect.Array:
		out := make([]reflect.Value, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			elem := v.Index(i)
			for elem.Kind() == reflect.Ptr {
				elem = elem.Elem()
			}
			if elem.Kind() == reflect.Struct {
				out = append(out, elem)
			}
		}
		return out
	}
	return nil
}

// _ guards against schema being unused if reflect-only paths land below.
var _ schema.Field

// _ to keep fts.Query import linked through this file for the API; the
// callback file doesn't reference fts directly today.
var _ fts.Query
