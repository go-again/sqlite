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
package fts
