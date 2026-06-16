// Package gormbridge holds the reflect/gorm plumbing shared by the two
// gorm bridge sub-packages, vecgorm (vec/gorm) and ftsgorm (fts/gorm).
//
// These helpers are pure infrastructure — reflect-driven type
// indirection, callback context extraction, row enumeration, primary-key
// coercion, soft-delete field discovery, and connection-pool detection.
// They carry NO vec- or fts-specific logic on purpose: the two bridges
// historically maintained byte-for-byte copies (the fts copies even
// carried comments noting they mirror the vec ones), which drifted as a
// liability. Centralizing them here keeps the two bridges in lockstep and
// gives a single point of repair if gorm's reflect-facing contracts move.
//
// Only this module may import the package (it lives under internal/).
package gormbridge

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/go-again/sqlite/internal/sqlid"
)

// IndirectType strips pointers, slices, and arrays so reflect.TypeOf(&[]Doc{})
// and reflect.TypeOf(Doc{}) both resolve to the underlying struct. Used to
// key the per-bridge schema-metadata caches on a single canonical type.
func IndirectType(t reflect.Type) reflect.Type {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			t = t.Elem()
		default:
			return t
		}
	}
}

// HelperContext returns a context for callbacks that don't receive one
// explicitly. gorm threads the caller's ctx through Statement.Context;
// when absent (e.g. a bare db.Create with no WithContext) we fall back to
// context.Background so downstream ExecContext calls still have a ctx.
func HelperContext(db *gorm.DB) context.Context {
	if db.Statement != nil && db.Statement.Context != nil {
		return db.Statement.Context
	}
	return context.Background()
}

// deletedAtType is the concrete type gorm uses for soft-delete columns; we
// match on it so models that rename the Go field (e.g.
// `RemovedAt gorm.DeletedAt` or `ArchivedAt gorm.DeletedAt`) still
// participate in sidecar soft-delete sync. The previous
// `LookUpField("DeletedAt")` discipline only matched the default field
// name and silently missed renamed fields.
var deletedAtType = reflect.TypeFor[gorm.DeletedAt]()

// FindDeletedAtField returns the schema's gorm.DeletedAt field regardless
// of its Go name, or nil if the model has no soft-delete field at all.
func FindDeletedAtField(s *schema.Schema) *schema.Field {
	if s == nil {
		return nil
	}
	for _, f := range s.Fields {
		if f.StructField.Type == deletedAtType {
			return f
		}
	}
	return nil
}

// PKAsInt64 reads the primary-key field off a row and converts to int64.
// Returns false for unsupported types (composite PK fields land here too
// because the bridges already error in registerSchema on non-single PK
// setups). Used to derive the vec0 / FTS5 rowid from the gorm model's PK.
func PKAsInt64(f *schema.Field, row reflect.Value) (int64, bool) {
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

// IterateRows normalizes db.Statement.ReflectValue (either a struct or a
// slice/array of structs depending on the call form) into a flat
// []reflect.Value addressing each row, so callbacks can treat single-row
// and batch writes uniformly.
func IterateRows(v reflect.Value) []reflect.Value {
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

// ExecPool is the subset of gorm.ConnPool the bridges need to issue
// sidecar / FTS5 statements. Both *sql.DB and *sql.Tx satisfy it; using
// this interface in callbacks ensures writes participate in any active
// gorm.Transaction rather than auto-committing through the parent *sql.DB.
type ExecPool interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// MaterializeByRowid fetches the gorm models whose primary key is in
// rowids and returns them keyed by PK as int64. The fetch runs through
// the caller's db so scopes, preloads, and session config carry through;
// the PK column is backtick-quoted to match the bridges' other generated
// SQL. gorm's `IN` clause does not preserve order on SQLite, so the
// result is a lookup map — callers reassemble in their own ranking order
// and build their own per-package Hit type. Only this fetch-and-index
// step (a reflect dance that was byte-identical in both bridges, modulo
// the error prefix) is shared; the returned error is unwrapped so callers
// add their "vecgorm:" / "ftsgorm:" prefix.
//
// A PK present in rowids but absent from the fetched rows is silently
// dropped from the map — callers treat a missing key as a stale sidecar
// entry and skip it rather than failing the whole search.
func MaterializeByRowid[T any](ctx context.Context, db *gorm.DB, pkField *schema.Field, rowids []any) (map[int64]T, error) {
	return MaterializeByKey[int64, T](ctx, db, pkField, rowids, PKAsInt64)
}

// PKAsString extracts the primary key as a string, unwrapping pointers. Returns
// ("", false) for a nil pointer or a non-string kind.
func PKAsString(f *schema.Field, row reflect.Value) (string, bool) {
	v := row.FieldByIndex(f.StructField.Index)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.String {
		return v.String(), true
	}
	return "", false
}

// MaterializeByKey is the generic form of [MaterializeByRowid]: it fetches the
// rows whose primary key is in keys and returns them indexed by the key
// extracted via keyOf (so callers can key by int64 rowid or string PK). A key
// present in keys but absent from the fetched rows is silently dropped (treated
// as a stale sidecar entry).
func MaterializeByKey[K comparable, T any](ctx context.Context, db *gorm.DB, pkField *schema.Field, keys []any, keyOf func(*schema.Field, reflect.Value) (K, bool)) (map[K]T, error) {
	var zero T
	models := reflect.New(reflect.SliceOf(reflect.TypeOf(zero))).Interface()
	if err := db.WithContext(ctx).
		Where(fmt.Sprintf("%s IN ?", sqlid.QuoteIdentBacktick(pkField.DBName)), keys).
		Find(models).Error; err != nil {
		return nil, err
	}
	indexed := make(map[K]T, len(keys))
	sliceVal := reflect.ValueOf(models).Elem()
	for i := 0; i < sliceVal.Len(); i++ {
		row := sliceVal.Index(i)
		k, ok := keyOf(pkField, row)
		if !ok {
			continue
		}
		indexed[k] = row.Interface().(T)
	}
	return indexed, nil
}

// ActivePool returns the connection pool the gorm.DB is currently using.
// Inside a gorm transaction (db.Transaction / db.Begin) this is the active
// *sql.Tx, so bridge writes commit or roll back with the parent. Outside a
// transaction it is the underlying *sql.DB.
func ActivePool(db *gorm.DB) (ExecPool, error) {
	if db.Statement != nil && db.Statement.ConnPool != nil {
		if p, ok := db.Statement.ConnPool.(ExecPool); ok {
			return p, nil
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("gormbridge: unable to obtain ConnPool: %w", err)
	}
	return sqlDB, nil
}
