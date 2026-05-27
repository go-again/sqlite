// Package fts is a typed, generics-aware wrapper around SQLite's FTS5
// full-text-search virtual table, layered on top of github.com/go-again/sqlite.
//
// FTS5 is compiled into modernc.org/sqlite/lib (this package's underlying
// engine) by default — no extension to load. Blank-importing the parent
// driver is sufficient:
//
//	import (
//	    "database/sql"
//	    _ "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/fts"
//	)
//
// # Quick start
//
//	db, _ := sql.Open("sqlite3", ":memory:")
//	idx, _ := fts.New[int64, string](ctx, db, "docs", fts.Options{
//	    Tokenizer: fts.Porter{Base: fts.Unicode61{}},
//	})
//	idx.Insert(ctx,
//	    fts.Attr[int64, string]{Key: 1, Value: "the quick brown fox"},
//	    fts.Attr[int64, string]{Key: 2, Value: "jumped over the lazy dog"},
//	)
//	for m, err := range idx.Search(ctx, fts.Term("fox")) {
//	    if err != nil { ... }
//	    fmt.Println(m.Key, m.Value, m.Rank)
//	}
//
// # Content-less / external-content tables
//
// Pass Options.External to back an FTS5 index by an existing table; updates
// to the source table do not propagate automatically — call Index.Rebuild
// when the underlying content changes. See https://www.sqlite.org/fts5.html
// section 4.4 for details on FTS5's external-content mode.
//
// # Observability
//
// Search/Insert/Delete operations can be wrapped with slog logging, an OTEL
// trace span, or a metrics recorder by composing the optional decorators in
// observability.go.
//
// # See also
//
//   - github.com/go-again/sqlite/fts/gorm — tag-driven FTS5 indexes
//     wired into gorm models. Tag string fields with fts5:"…" and the
//     plugin manages CREATE, AFTER INSERT/UPDATE/DELETE triggers,
//     external / in-table / contentless mode selection, soft-delete
//     filtering, and DropTable cascade.
//   - examples/fts-search — runnable demo of the raw fts.Index API.
//   - examples/gorm-fts-tagged — the same flow expressed via the
//     gorm bridge.
//   - docs/coverage-fts.md — every documented FTS5 feature with its
//     current status (typed / raw / inherited).
package fts
