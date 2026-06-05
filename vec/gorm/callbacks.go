package vecgorm

import (
	"fmt"
	"reflect"

	"github.com/go-again/sqlite/internal/gormbridge"
	"github.com/go-again/sqlite/vec"
	"gorm.io/gorm"
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
	rows := gormbridge.IterateRows(db.Statement.ReflectValue)
	if len(rows) == 0 {
		return
	}

	ctx := gormbridge.HelperContext(db)
	for _, m := range mm.Fields {
		items := make([]vec.Row, 0, len(rows))
		for _, row := range rows {
			rowid, ok := gormbridge.PKAsInt64(mm.PKField, row)
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
			items = append(items, vec.Row{Rowid: rowid, Embedding: emb})
		}
		if len(items) == 0 {
			continue
		}
		if err := batchInsertEmbeddings(ctx, db, m, items); err != nil {
			_ = db.AddError(err)
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
	rows := gormbridge.IterateRows(db.Statement.ReflectValue)
	if len(rows) == 0 {
		return
	}

	ctx := gormbridge.HelperContext(db)
	for _, m := range mm.Fields {
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

		for _, row := range rows {
			rowid, ok := gormbridge.PKAsInt64(mm.PKField, row)
			if !ok {
				continue
			}
			emb, ok := embeddingFrom(row, m.FieldIndex)
			if !ok || len(emb) == 0 {
				continue
			}
			if err := updateEmbedding(ctx, db, m, rowid, emb); err != nil {
				_ = db.AddError(err)
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
	rows := gormbridge.IterateRows(db.Statement.ReflectValue)

	ctx := gormbridge.HelperContext(db)
	for _, m := range mm.Fields {
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
			if err := deleteByWhere(ctx, db, mm, m); err != nil {
				_ = db.AddError(err)
				return
			}
			continue
		}

		// Non-soft-delete models: straight delete from sidecar.
		if len(rows) == 0 {
			if err := deleteByWhere(ctx, db, mm, m); err != nil {
				_ = db.AddError(err)
				return
			}
			continue
		}
		for _, row := range rows {
			rowid, ok := gormbridge.PKAsInt64(mm.PKField, row)
			if !ok {
				continue
			}
			if err := deleteEmbedding(ctx, db, m, rowid); err != nil {
				_ = db.AddError(err)
				return
			}
		}
	}
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
	// Fast path: the common case is a field declared as exactly
	// `[]float32` (or `Embedding` which is a `type Embedding []float32`
	// — also kind=Slice with elem Kind=Float32 and Interface convertible).
	// Skip the per-element reflect.Value walk.
	if emb, ok := v.Interface().([]float32); ok {
		// Defensive copy: caller may stash this past the source row's
		// lifetime, and gorm reuses row buffers between iterations.
		out := make([]float32, len(emb))
		copy(out, emb)
		return out, true
	}
	out := make([]float32, v.Len())
	for i := 0; i < v.Len(); i++ {
		out[i] = float32(v.Index(i).Float())
	}
	return out, true
}
