// Package ftsgorm provides a tag-driven bridge between gorm models
// and SQLite FTS5 search indexes. Annotate one or more text fields
// with `fts5:"…"`, register the plugin via db.Use, and the package
// owns the index lifecycle: CREATE on Migrate, AFTER INSERT/UPDATE/
// DELETE triggers on the source (external-content mode, the default),
// optional in-table or contentless modes, soft-delete filtering, and
// DROP that cascades automatically when callers run
// db.Migrator().DropTable.
//
// # Quick start
//
//	import (
//	    sqlitegorm "github.com/go-again/sqlite/gorm"
//	    "github.com/go-again/sqlite/fts"
//	    ftsgorm "github.com/go-again/sqlite/fts/gorm"
//	    "gorm.io/gorm"
//	)
//
//	type Article struct {
//	    ID    uint   `gorm:"primaryKey"`
//	    Title string `fts5:"tokenize=porter+unicode61"`
//	    Body  string `fts5:"tokenize=porter+unicode61"`
//	}
//
//	db, _ := gorm.Open(sqlitegorm.Open("app.db"), &gorm.Config{})
//	db.Use(ftsgorm.Plugin())
//	ftsgorm.Migrate(db, &Article{})   // creates articles + articles_fts + triggers
//
//	db.Create(&Article{Title: "Hello", Body: "the quick brown fox"})
//
//	results, _ := ftsgorm.Search[Article](ctx, db, fts.Term("fox"),
//	    ftsgorm.WithSnippet("body", "<b>", "</b>", "…", 8),
//	    ftsgorm.WithHighlight("body", "[", "]"),
//	    ftsgorm.WithRanking(2.0, 1.0),
//	)
//
//	db.Migrator().DropTable(&Article{})  // source + FTS5 table + triggers gone
//
// # Tag syntax
//
// All keys are optional. Separator is `;`. Spaces inside a tag value
// are escaped as `+` (gorm's tag parser handles unquoted spaces
// inconsistently).
//
//	Key                              | Meaning                              | Default
//	---------------------------------+--------------------------------------+--------------
//	tokenize=NAME[+args]             | FTS5 tokenize= option                | unicode61
//	prefix=N1,N2,...                 | Pre-computed prefix-match indexes    | none
//	column=NAME                      | Override FTS5 column name            | lowercase field name
//	table=NAME                       | Shared FTS5 table name               | <source>_fts
//	detail=full | column | none      | FTS5 detail= option                  | full
//	external=true | false            | External-content mode                | true
//	contentless=true                 | Contentless FTS5 (index only)        | (off)
//
// Multiple `fts5:`-tagged fields on one model share ONE FTS5 table.
// Conflicting table-level keys across fields (e.g. two fields
// declaring different `tokenize=` values) are rejected at Migrate.
//
// # Modes
//
// The mode controls how the FTS5 table relates to the gorm source:
//
//   - ModeExternal (default; `external=true`) — the FTS5 table is
//     declared `content='source_table'` so FTS5 does not store text
//     itself; values come from the source on demand. AFTER INSERT /
//     UPDATE / DELETE triggers on the source maintain the index, so
//     even raw SQL writes stay in sync. snippet() and highlight()
//     work because the source still has the text.
//
//   - ModeInTable (`external=false`) — the FTS5 table stores its own
//     copy of the indexed text. Row-level callbacks (not triggers)
//     keep the table in sync. Slightly cheaper to search; doubles
//     storage; snippet/highlight work.
//
//   - ModeContentless (`contentless=true`) — the FTS5 table stores
//     only the inverted index. No text means no snippet() and no
//     highlight() — Search returns an error if WithSnippet or
//     WithHighlight is requested with a contentless model. Cheapest
//     storage. Mutually exclusive with `external=true`.
//
// # Search options
//
// Search[T] accepts these options:
//
//   - WithLimit(n) / WithOffset(n)
//   - WithRanking(weights ...float64) — per-column BM25 weights
//   - WithSnippet(column, before, after, ellipsis, tokens int)
//   - WithHighlight(column, before, after)
//   - IncludeDeleted() — disables the default soft-delete filter
//
// # Soft-delete
//
// When the model uses gorm.DeletedAt, Migrate adds an UNINDEXED
// `deleted_at` mirror column (external mode) or a boolean `deleted`
// flag (in-table / contentless modes) to the FTS5 table. Search
// excludes soft-deleted rows by default. IncludeDeleted() surfaces
// them anyway. Hard deletes (db.Unscoped().Delete) remove the FTS5
// entry entirely via the AFTER DELETE trigger (external) or the
// Delete callback (in-table / contentless).
//
// # DropTable cascade
//
// fts/gorm's Plugin implements the DropTableHook interface declared
// in github.com/go-again/sqlite/gorm. db.Migrator().DropTable(&Model{})
// drops the source plus the FTS5 table plus all three triggers
// (external mode) without anyone calling DropSidecar separately.
// Explicit DropSidecar(db, &Model{}) remains available and is
// idempotent against the cascade.
//
// # Side-by-side compatibility
//
// ftsgorm is layered on top of github.com/go-again/sqlite/fts. The
// raw fts.Index API remains available for callers who want lower-level
// control (custom tokenizers, multi-index models, manual Rebuild
// after bulk loads). Both can coexist on the same *gorm.DB.
//
// # See also
//
//   - github.com/go-again/sqlite/fts — raw FTS5 API.
//   - github.com/go-again/sqlite/gorm — gorm dialector and the
//     DropTableHook interface this package implements.
//   - examples/features/gorm/fts-tagged — end-to-end demo of this package.
//   - dev/coverage/gorm.md — tag syntax tables, mode tradeoffs,
//     lifecycle matrix.
package ftsgorm
