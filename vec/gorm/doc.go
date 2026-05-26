// Package vecgorm provides a tag-driven bridge between gorm models and
// sqlite-vec virtual tables. Users tag a []float32 field on their model
// with a `vec:"…"` struct tag, register the plugin via db.Use, and get
// automatic sidecar lifecycle (CREATE / DROP), CRUD sync, and a typed
// KNN[T] helper that returns gorm models in ranking order.
//
// # Quick start
//
//	type Document struct {
//	    ID        uint   `gorm:"primaryKey"`
//	    Title     string
//	    // gorm:"-" is required so gorm's schema parser skips the []float32
//	    // column on the source table; vecgorm stores the embedding in
//	    // the sidecar vec0 table instead.
//	    Embedding []float32 `gorm:"-" vec:"dim=384;metric=cosine;encoding=binary"`
//	}
//
//	db.Use(vecgorm.Plugin())            // install once
//	vecgorm.Migrate(db, &Document{})    // creates documents + documents_vec
//	db.Create(&Document{...})           // auto-populates the sidecar
//
//	results, _ := vecgorm.KNN[Document](ctx, db, queryVec, 5)
//
// # Tag syntax
//
// All keys after `vec:` are optional except `dim`. Separator is `;`.
//
//	vec:"dim=384"                                     // minimum
//	vec:"dim=384;metric=cosine"                       // override metric
//	vec:"dim=384;encoding=binary"                     // binary wire encoding
//	vec:"dim=384;table=my_vec;column=embedding"       // override names
//
// # Side-by-side compatibility
//
// vecgorm is built on top of github.com/go-again/sqlite/vec. Code that
// already uses vec.Create / vec.Table directly continues to work; this
// package only adds a thinner gorm-integrated path. The two can coexist
// in the same process.
package vecgorm
