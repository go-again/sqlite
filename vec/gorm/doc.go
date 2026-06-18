// Package vecgorm provides a tag-driven bridge between gorm models
// and sqlite-vec virtual-table sidecars. Annotate a field on a gorm
// model with `vec:"…"`, register the plugin via db.Use, and the
// package owns the full sidecar lifecycle: CREATE on Migrate, CRUD
// sync on Create/Save/Update/Delete (single transaction per batch),
// soft-delete filtering when the model uses gorm.DeletedAt, and
// DROP that cascades automatically when callers run
// db.Migrator().DropTable.
//
// # Quick start
//
//	import (
//	    sqlitegorm "github.com/go-again/sqlite/gorm"
//	    vecgorm "github.com/go-again/sqlite/vec/gorm"
//	    "gorm.io/gorm"
//	)
//
//	type Document struct {
//	    ID        uint              `gorm:"primaryKey"`
//	    Title     string
//	    Embedding vecgorm.Embedding `vec:"dim=384;metric=cosine"`
//	}
//
//	db, _ := gorm.Open(sqlitegorm.Open("app.db"), &gorm.Config{})
//	db.Use(vecgorm.Plugin())
//	vecgorm.Migrate(db, &Document{})        // creates documents + documents_vec
//
//	db.Create(&Document{...})               // sidecar populated automatically
//
//	results, _ := vecgorm.KNN[Document](ctx, db, queryVec, 5)
//	for _, r := range results {
//	    fmt.Println(r.Model.ID, r.Distance) // returned in ranking order
//	}
//
//	db.Migrator().DropTable(&Document{})    // source + sidecar both gone
//
// # Primary keys
//
// Both integer and string primary keys are supported. An integer PK keys the
// sidecar on the implicit int64 rowid; a string PK (e.g. a UUID or slug) keys
// it on an explicit `id text primary key` column backed by a
// [github.com/go-again/sqlite/vec.KeyedTable]. The model's PK type is detected
// at registration — no tag is needed. Composite or non-int/non-string primary
// keys are not supported.
//
// # Tag syntax
//
// All keys after `vec:` are optional except `dim`. Separator is `;`.
//
//	Key                          | Meaning                              | Default
//	-----------------------------+--------------------------------------+-------------------
//	dim=N                        | Embedding dimension (required)       | —
//	metric=l2 | cosine | dot     | Distance metric                      | l2
//	encoding=json | binary       | Wire encoding                        | binary
//	table=NAME                   | Override sidecar table name          | <source>_vec
//	column=NAME                  | Override embedding column            | embedding
//
// Example combinations:
//
//	vec:"dim=384"                                     // minimum
//	vec:"dim=384;metric=cosine"
//	vec:"dim=384;encoding=binary;table=docs_emb"
//	vec:"dim=384;column=feat"
//
// # Field type: Embedding wrapper or []float32 + gorm:"-"
//
// gorm's schema parser rejects raw []float32 because it has no SQL
// type for it. Two ways to feed it a type it accepts:
//
//	// Recommended — wrapper type satisfies gorm's GormDataType:
//	Embedding vecgorm.Embedding `vec:"dim=384"`
//
//	// Equivalent — bare slice with gorm:"-" to skip schema parsing:
//	Embedding []float32 `gorm:"-" vec:"dim=384"`
//
// The plugin always sets IgnoreMigration=true on the tagged field,
// so the source table never gets an embedding column regardless of
// which form you choose. If both escape hatches are missing, the
// plugin emits a clear error at Migrate time.
//
// # KNN options
//
// KNN[T] accepts these options:
//
//   - WithFilter(sql, args...) — extra WHERE conjunct on the sidecar
//     (typically against metadata columns the user declared via raw
//     SQL; sqlite-vec accepts rowid filters too but may bypass the
//     ANN planner for them).
//   - IncludeDeleted() — disables the default `deleted = 0` filter
//     on models that have gorm.DeletedAt.
//
// # Soft-delete
//
// When the model uses gorm.DeletedAt, Migrate adds a `deleted`
// integer metadata column to the vec0 sidecar. Callbacks flip the
// flag to 1 on soft delete and KNN excludes deleted rows by default.
// Hard deletes (db.Unscoped().Delete) remove the sidecar row.
// IncludeDeleted() surfaces them anyway.
//
// # DropTable cascade
//
// vec/gorm's Plugin implements the DropTableHook interface declared
// in github.com/go-again/sqlite/gorm. When callers run
// db.Migrator().DropTable(&Model{}), our Migrator iterates plugins
// and invokes the hook before the source DROP, so the vec0 sidecar
// goes away without anyone calling DropSidecar separately.
//
// Explicit DropSidecar(db, &Model{}) is still available and is
// idempotent against the cascade.
//
// # Side-by-side compatibility
//
// vecgorm is layered on top of github.com/go-again/sqlite/vec. The
// raw vec.Table API remains available for callers who want to
// manage sidecars by hand or use features the bridge does not expose
// (bit / int8 vectors, partition columns, auxiliary columns).
// Both styles coexist on the same *gorm.DB.
//
// # See also
//
//   - github.com/go-again/sqlite/vec — raw vector-search API.
//   - github.com/go-again/sqlite/gorm — gorm dialector (Open, New,
//     Dialector, the DropTableHook interface).
//   - examples/features/gorm/vec-tagged — end-to-end demo of this package.
//   - docs/coverage-gorm.md — tag syntax tables and lifecycle matrix.
package vecgorm
