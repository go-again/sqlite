// Package ftsgorm provides a tag-driven bridge between gorm models and
// SQLite FTS5 external-content indexes. Users tag string fields with
// `fts5:"…"` struct tags, register the plugin via db.Use, and get
// automatic FTS5 table creation, AFTER INSERT/UPDATE/DELETE triggers
// that keep the index in sync, and a typed Search[T] helper.
//
// # Quick start
//
//	type Article struct {
//	    ID    uint   `gorm:"primaryKey"`
//	    Title string `fts5:"tokenize=porter+unicode61"`
//	    Body  string `fts5:"tokenize=porter+unicode61"`
//	}
//
//	db.Use(ftsgorm.Plugin())
//	ftsgorm.Migrate(db, &Article{})  // creates articles + articles_fts + triggers
//	db.Create(&Article{Title: "Hello", Body: "world"})
//	results, _ := ftsgorm.Search[Article](ctx, db, fts.Term("hello"))
//
// # Tag syntax
//
// All keys are optional; separator is `;`. Spaces inside a tag value
// are escaped as `+` (gorm's tag parser handles spaces inconsistently).
//
//	fts5:"tokenize=porter+unicode61"     // Porter stemmer wrapping unicode61
//	fts5:"tokenize=ascii"                // ASCII tokenizer
//	fts5:"prefix=2,3,4"                  // pre-computed prefix indexes
//	fts5:"detail=column"                 // skip position info; smaller index
//	fts5:"column=headline"               // override the FTS5 column name
//	fts5:"table=article_search"          // override the shared FTS5 table
//
// All fields tagged on one model share ONE FTS5 table. If different
// fields on the same model declare conflicting table-level keys
// (table, tokenize, prefix, detail) the plugin errors at registration.
//
// # Soft-delete handling
//
// Models using gorm.DeletedAt are detected at registration. Search
// automatically filters out soft-deleted rows; pass IncludeDeleted()
// to override.
//
// # Side-by-side compatibility
//
// ftsgorm is built on top of github.com/go-again/sqlite/fts; the
// raw fts.Index API remains available for callers who want lower-level
// control. Both can coexist on the same *sql.DB.
package ftsgorm
