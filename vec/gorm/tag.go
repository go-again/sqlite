package vecgorm

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"gosqlite.org/vec"
)

// TagName is the struct tag key we look for. We deliberately do NOT
// put our directives inside the gorm:"" tag — owning a separate
// namespace keeps us out of gorm's parser and avoids any future
// keyword collision.
const TagName = "vec"

// Exported tag-key constants. The parser reads keys via these so
// callers consuming the tag DSL (and tests pinning the surface) can
// reference the canonical names without re-hardcoding the strings.
const (
	TagKeyDim      = "dim"
	TagKeyMetric   = "metric"
	TagKeyEncoding = "encoding"
	TagKeyTable    = "table"
	TagKeyColumn   = "column"
)

// tagName is the internal alias retained so the existing parser body
// keeps reading without churn; same value as TagName.
const tagName = TagName

// meta is the parsed form of a vec:"..." struct tag attached to a model
// field. Each meta describes one sidecar virtual table.
type meta struct {
	// FieldIndex points into the gorm-parsed Schema.Fields[*].StructField.Index
	// so the plugin can find the embedding value during callbacks. Stored
	// as the int chain for use with reflect.Value.FieldByIndex.
	FieldIndex []int

	// FieldName is the Go-side field name on the model (e.g. "Embedding").
	FieldName string

	// Dim is the declared dimension. Required.
	Dim int

	// Metric / Encoding map to vec.Options.
	Metric   vec.Metric
	Encoding vec.Encoding

	// Table is the sidecar vec0 table name. Defaults to "<source>_vec"
	// where <source> is the gorm table name; the plugin fills the default
	// at registration time because tag parsing doesn't know it yet.
	Table string

	// Column is the embedding column name inside the sidecar.
	Column string

	// SoftDelete is set to true at plugin-registration time when the
	// model's gorm schema has a soft-delete column. The sidecar carries
	// a `deleted` metadata column and KNN filters on it by default.
	SoftDelete bool

	// KeyColumn / KeyIsText describe the sidecar's primary-key column,
	// resolved at registration from the model's gorm PK type. Integer PKs
	// use the implicit "rowid" (KeyIsText=false); string PKs use an explicit
	// "id text primary key" column (KeyIsText=true), backed by a
	// vec.KeyedTable[string]. Stamped onto every field's meta because the
	// per-field sidecar functions take only meta, not the model's PK info.
	KeyColumn string
	KeyIsText bool
}

// parseTag decodes a vec:"..." tag value into a meta. The fieldName /
// fieldIndex are filled in by the caller. dim is required; everything
// else has defaults defined in the package doc.
//
// Encoding default note: vec.Options{} (the raw-vec API) defaults to
// vec.JSON for backwards compatibility with the sqlite-vec docs;
// vec/gorm tags default to vec.Binary because tag-driven users
// usually want the faster wire format and we own the codepath end-to-
// end. Override with `vec:"...;encoding=json"` when round-tripping
// with the raw-vec API or sqlite-vec text examples.
func parseTag(tagValue, fieldName string, fieldIndex []int) (meta, error) {
	m := meta{
		FieldName:  fieldName,
		FieldIndex: append([]int(nil), fieldIndex...),
		Metric:     vec.L2,
		Encoding:   vec.Binary,
		Column:     "embedding",
	}
	if tagValue == "" {
		return m, fmt.Errorf("vecgorm: %s: empty vec: tag", fieldName)
	}
	for kv := range strings.SplitSeq(tagValue, ";") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			return m, fmt.Errorf("vecgorm: %s: tag entry %q is not key=value", fieldName, kv)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// Tag values that would otherwise need spaces use '+' as the
		// escape; translate here so downstream consumers see plain
		// text. (Currently only relevant for fts5: tags, but we apply
		// the same convention here for forward compatibility.)
		val = strings.ReplaceAll(val, "+", " ")

		switch key {
		case TagKeyDim:
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return m, fmt.Errorf("vecgorm: %s: dim=%q must be a positive integer", fieldName, val)
			}
			m.Dim = n
		case TagKeyMetric:
			metric, err := vec.ParseMetric(val)
			if err != nil {
				return m, fmt.Errorf("vecgorm: %s: %w", fieldName, err)
			}
			m.Metric = metric
		case TagKeyEncoding:
			enc, err := vec.ParseEncoding(val)
			if err != nil {
				return m, fmt.Errorf("vecgorm: %s: %w", fieldName, err)
			}
			m.Encoding = enc
		case TagKeyTable:
			if !isIdent(val) {
				return m, fmt.Errorf("vecgorm: %s: table=%q is not a valid identifier", fieldName, val)
			}
			m.Table = val
		case TagKeyColumn:
			if !isIdent(val) {
				return m, fmt.Errorf("vecgorm: %s: column=%q is not a valid identifier", fieldName, val)
			}
			m.Column = val
		default:
			return m, fmt.Errorf("vecgorm: %s: unknown tag key %q", fieldName, key)
		}
	}
	if m.Dim == 0 {
		return m, fmt.Errorf("vecgorm: %s: dim=N is required", fieldName)
	}
	return m, nil
}

// preflightTags walks rt's fields BEFORE handing the type to gorm's
// schema.Parse. For every field with our vec:"..." tag, it confirms the
// field is parseable by gorm. Two forms are accepted:
//
//  1. Field type is vecgorm.Embedding (or a named type whose underlying
//     type is vecgorm.Embedding). The wrapper satisfies gorm's
//     GormDataType interface so Parse succeeds without further help.
//  2. Field has `gorm:"-"` (or a `gorm:"-:..."` variant). gorm skips the
//     field entirely during schema parsing.
//
// Other shapes — bare []float32 without either signal — would crash
// gorm.schema.Parse with "unsupported data type: &[]". We emit a clear
// error here so users see a hint, not gorm's cryptic message.
func preflightTags(rt reflect.Type) error {
	if rt.Kind() != reflect.Struct {
		return nil
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			if err := preflightTags(f.Type); err != nil {
				return err
			}
			continue
		}
		if _, ok := f.Tag.Lookup(tagName); !ok {
			continue
		}
		// Accept any field whose type implements GormDataType — that
		// includes our Embedding wrapper and any user-defined alias
		// (e.g. `type Vec []float32` with their own GormDataType).
		if hasGormDataType(f.Type) {
			continue
		}
		// Otherwise require gorm:"-".
		gormTag := f.Tag.Get("gorm")
		if !hasGormIgnore(gormTag) {
			return fmt.Errorf(
				"vecgorm: %s.%s has a vec:\"...\" tag but is missing gorm:\"-\" "+
					"and is not declared as vecgorm.Embedding — either change the "+
					"field type to vecgorm.Embedding (preferred) or add gorm:\"-\" "+
					"so gorm's schema parser doesn't reject the unknown []float32 type",
				rt.Name(), f.Name)
		}
	}
	return nil
}

// hasGormDataType reports whether t (or *t, if t isn't a pointer)
// implements the GormDataType() string interface gorm consults during
// schema parsing.
func hasGormDataType(t reflect.Type) bool {
	type dataTyper interface{ GormDataType() string }
	if _, ok := reflect.Zero(t).Interface().(dataTyper); ok {
		return true
	}
	if t.Kind() != reflect.Pointer {
		pt := reflect.PointerTo(t)
		if _, ok := reflect.Zero(pt).Interface().(dataTyper); ok {
			return true
		}
	}
	return false
}

// hasGormIgnore reports whether a gorm:"..." tag value tells gorm to
// skip the field entirely. gorm accepts "-", "-:all", and "-:migration"
// among other "-:" forms; for our purposes any "-" entry is sufficient.
func hasGormIgnore(tag string) bool {
	for entry := range strings.SplitSeq(tag, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "-" {
			return true
		}
		if strings.HasPrefix(entry, "-:") {
			return true
		}
	}
	return false
}

// isIdent is a thin alias for vec.ValidIdent. We keep the local name
// so call sites stay readable; the real implementation lives in vec
// so the rule is defined exactly once across the module.
func isIdent(s string) bool { return vec.ValidIdent(s) }
