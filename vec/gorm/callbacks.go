package vecgorm

import (
	"fmt"
	"reflect"

	"github.com/go-again/sqlite/vec"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// afterCreate runs after the source-table INSERT. Reads the just-assigned
// rowid from the gorm Statement and writes the matching embedding to
// every sidecar declared on the model. Supports both single-row and
// batch Create paths (gorm calls the callback once per Create with the
// full slice in ReflectValue).
func (p *plugin) afterCreate(db *gorm.DB) {
	mm, ok := p.lookupForDB(db)
	if !ok {
		return
	}
	rows := iterateRows(db.Statement.ReflectValue)
	if len(rows) == 0 {
		return
	}

	for _, m := range mm.Fields {
		items := make([]vec.Item, 0, len(rows))
		for _, row := range rows {
			rowid, ok := pkAsInt64(mm.PKField, row)
			if !ok {
				_ = db.AddError(fmt.Errorf(
					"vecgorm: %s primary key value %v is not convertible to int64",
					mm.PKField.Name, row.FieldByIndex(mm.PKField.StructField.Index).Interface()))
				return
			}
			emb, ok := embeddingFrom(row, m.FieldIndex)
			if !ok || len(emb) == 0 {
				// Empty embedding: skip — caller may populate later
				// via Save. (Matches gorm's "zero values get default
				// or NULL" convention.)
				continue
			}
			items = append(items, vec.Item{Rowid: rowid, Embedding: emb})
		}
		if len(items) == 0 {
			continue
		}
		if m.SoftDelete {
			// vec0 INTEGER metadata columns reject NULL; the typed
			// BatchInsert only writes (rowid, embedding) so the
			// deleted column would be NULL. We do our own INSERT
			// passing deleted=0 alongside.
			if err := batchInsertWithSoftDelete(db, m, items); err != nil {
				_ = db.AddError(err)
				return
			}
			continue
		}
		tbl, err := openSidecar(db, m)
		if err != nil {
			_ = db.AddError(err)
			return
		}
		if err := tbl.BatchInsert(helperContext(db), items); err != nil {
			_ = db.AddError(fmt.Errorf("vecgorm: BatchInsert into %s: %w", m.Table, err))
			return
		}
	}
}

// afterUpdate runs after a row UPDATE. We re-issue Update on the
// sidecar for each tagged field whose value changed (we don't track
// dirtiness; we just write through). For soft-delete via gorm's
// DeletedAt, gorm's "delete" path is an UPDATE that sets deleted_at —
// we detect this and flip the sidecar's deleted flag to 1.
func (p *plugin) afterUpdate(db *gorm.DB) {
	mm, ok := p.lookupForDB(db)
	if !ok {
		return
	}
	rows := iterateRows(db.Statement.ReflectValue)
	if len(rows) == 0 {
		return
	}

	for _, m := range mm.Fields {
		tbl, err := openSidecar(db, m)
		if err != nil {
			_ = db.AddError(err)
			return
		}

		// Detect soft-delete: when gorm processed the row as a
		// soft-delete, db.Statement.Schema.LookUpField("DeletedAt")
		// has a value and the row's deleted_at is non-zero. We can't
		// see the dest row reliably here for batch updates, so fall
		// back to a query: any row whose source PK matches and whose
		// deleted_at is non-null gets `deleted=1` in the sidecar.
		if m.SoftDelete && isSoftDelete(db) {
			if err := softDeleteSidecar(db, mm, m); err != nil {
				_ = db.AddError(err)
				return
			}
			continue
		}

		ctx := helperContext(db)
		for _, row := range rows {
			rowid, ok := pkAsInt64(mm.PKField, row)
			if !ok {
				continue
			}
			emb, ok := embeddingFrom(row, m.FieldIndex)
			if !ok || len(emb) == 0 {
				continue
			}
			if err := tbl.Update(ctx, rowid, emb); err != nil {
				_ = db.AddError(fmt.Errorf("vecgorm: Update on %s: %w", m.Table, err))
				return
			}
		}
	}
}

// afterDelete handles both hard (Unscoped().Delete) and soft deletes.
// gorm runs the same callback chain for both — the soft-delete path
// issues an UPDATE on deleted_at under the hood, but our callback sees
// it as gorm:delete. We distinguish via the source table's row state:
// after the SQL has run, a soft-deleted row has deleted_at IS NOT NULL
// in the source, and we flip the sidecar's deleted=1 to match. A
// hard-deleted row is gone from the source, and we DELETE its sidecar
// entry.
func (p *plugin) afterDelete(db *gorm.DB) {
	mm, ok := p.lookupForDB(db)
	if !ok {
		return
	}
	rows := iterateRows(db.Statement.ReflectValue)

	for _, m := range mm.Fields {
		tbl, err := openSidecar(db, m)
		if err != nil {
			_ = db.AddError(err)
			return
		}
		ctx := helperContext(db)

		// Soft-delete-aware models: instead of DELETEing from the
		// sidecar, sync the deleted flag from the source. The source
		// already has deleted_at populated by gorm's prior UPDATE.
		// For hard deletes (gorm under Unscoped), the source rows
		// are gone — softDeleteSidecar's COALESCE → 0 path leaves
		// orphans alone; we cover that below with deleteByWhere.
		if m.SoftDelete {
			if err := softDeleteSidecar(db, mm, m); err != nil {
				_ = db.AddError(err)
				return
			}
			// If this was a hard-delete (no longer present in source),
			// the sync above would set deleted=0 via COALESCE default.
			// Also GC orphans so the sidecar matches the source's row
			// set.
			if err := deleteByWhere(ctx, db, mm, m, tbl); err != nil {
				_ = db.AddError(err)
				return
			}
			continue
		}

		// Non-soft-delete models: straight delete from sidecar.
		if len(rows) == 0 {
			if err := deleteByWhere(ctx, db, mm, m, tbl); err != nil {
				_ = db.AddError(err)
				return
			}
			continue
		}
		for _, row := range rows {
			rowid, ok := pkAsInt64(mm.PKField, row)
			if !ok {
				continue
			}
			if err := tbl.Delete(ctx, rowid); err != nil {
				_ = db.AddError(fmt.Errorf("vecgorm: Delete from %s: %w", m.Table, err))
				return
			}
		}
	}
}

// iterateRows normalizes db.Statement.ReflectValue (which is either a
// struct or a slice of structs depending on the call form) into a flat
// []reflect.Value addressing each row.
func iterateRows(v reflect.Value) []reflect.Value {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
		return []reflect.Value{v}
	case reflect.Slice, reflect.Array:
		out := make([]reflect.Value, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			elem := v.Index(i)
			for elem.Kind() == reflect.Pointer {
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

// pkAsInt64 reads the primary-key field off a row and converts to int64.
// Returns false for unsupported types (composite PK fields land here too
// because we already error in registerSchema on non-single PK setups).
func pkAsInt64(f *schema.Field, row reflect.Value) (int64, bool) {
	v := row.FieldByIndex(f.StructField.Index)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return 0, false
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(v.Uint()), true
	}
	return 0, false
}

// embeddingFrom reads a []float32 (or compatible) field off a row.
func embeddingFrom(row reflect.Value, index []int) ([]float32, bool) {
	v := row.FieldByIndex(index)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.Float32 {
		return nil, false
	}
	out := make([]float32, v.Len())
	for i := 0; i < v.Len(); i++ {
		out[i] = float32(v.Index(i).Float())
	}
	return out, true
}
