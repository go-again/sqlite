package vecgorm

// Embedding is the recommended field type for vec-tagged embeddings on
// gorm models. It is a plain []float32 alias; the value semantics are
// identical and the type is freely convertible to / from []float32.
//
// Why a wrapper at all? gorm's schema.Parse refuses to parse a raw
// []float32 field ("unsupported data type: &[]") because no SQL type
// is registered for slice-of-float. The Embedding wrapper implements
// the GormDataType interface gorm consults BEFORE its dialector's
// DataTypeOf, returning "BLOB" — that lets Parse succeed. The plugin
// then flips IgnoreMigration / Creatable / Updatable / Readable to
// false on every vec-tagged field, so the BLOB column is never
// actually created on the source table.
//
// Users can still declare `Embedding []float32` plus `gorm:"-"`; both
// forms are supported. The wrapper is just the path that doesn't
// require a second tag.
type Embedding []float32

// GormDataType returns the SQL type gorm uses for the source-table
// column. gorm's schema parser calls this before consulting the
// dialector's DataTypeOf when the field's type implements the
// interface. "BLOB" is a safe placeholder — the plugin marks the
// field IgnoreMigration immediately after Parse, so the column is
// never created.
func (Embedding) GormDataType() string { return "BLOB" }
