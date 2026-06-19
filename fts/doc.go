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
// # Key types
//
// The K type parameter is the FTS5 rowid. K may be any int / int32 /
// int64 / uint / uint32 / uint64 / float32 / float64 / string / []byte.
// int64 keys are preserved at full 63-bit precision (no float64
// detour), so snowflake IDs, unix-nano timestamps, and any other key
// above 2^53 survive the Search round-trip exactly.
//
// V is the column type for the indexed text; string is the canonical
// choice but []byte also works for raw-bytes columns.
//
// # Vocabulary tables
//
// [NewVocab] builds an fts5vocab view of an index's term dictionary for
// term-frequency analytics and autocomplete — [Vocab.Terms] / [Vocab.TopTerms]
// (per-term document and occurrence counts) and [Vocab.Instances] (per-token
// occurrences). Mirrors spellfix1.Vocab.
//
// # Maintenance and query operators
//
// Beyond [Index.Rebuild] / [Index.Optimize] / [Index.Merge], the index exposes
// [Index.IntegrityCheck], [Index.DeleteAll] (contentless / external-content),
// and [Index.SetRank] (default rank function). The query builder adds
// [ColumnSet] (`{a b} : query`) and [InitialToken] (`^token`) alongside Term /
// Phrase / Prefix / And / Or / Not / Near / Column.
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
// # Filtered search
//
// [WithFilter] AND's an arbitrary WHERE conjunct onto the MATCH
// expression. Use this for per-tenant, per-user, or other column-level
// filtering without dropping to raw SQL. Same trust contract as
// [gorm.DB.Where] — the fragment is interpolated as-is; pass literals
// through args.
//
// # Custom projection / JOIN
//
// When you need to project additional columns or JOIN a companion
// table (the common "FTS5 sidecar to a canonical row table" pattern),
// use [Index.SearchSQL] with [WithSelect], [WithJoin], and
// [WithOrderBy] to build the query, then execute it through
// `db.QueryContext` (or `gorm.DB.Raw(sql, args...).Scan(&out)`):
//
//	sql, args, _ := idx.SearchSQL(fts.Term("hello"),
//	    fts.WithSelect("items.id, items.title"),
//	    fts.WithJoin("JOIN items ON items.id = items_fts.rowid"),
//	    fts.WithFilter("items.tenant = ?", "acme"),
//	    fts.WithLimit(10),
//	)
//	rows, _ := db.QueryContext(ctx, sql, args...)
//
// [Index.Search] / [Index.SearchSlice] reject WithSelect / WithJoin
// because they change the row shape the typed scanner expects.
//
// # Content-less / external-content tables
//
// Pass Options.External to back an FTS5 index by an existing table.
// Set Options.External.SyncTriggers to [SyncAll] (or the bits you
// want) to install AFTER-INSERT / AFTER-UPDATE / AFTER-DELETE
// triggers on the source table — they keep the FTS5 index in sync
// with no caller code. Without SyncTriggers the caller is responsible
// for sync (manual triggers or [Index.Rebuild]). See
// https://www.sqlite.org/fts5.html section 4.4 for details on FTS5's
// external-content mode.
//
// # Idempotent migrations
//
// [New] errors with [ErrAlreadyExists] (wrapped) if the named table
// already exists. Pass [WithIfNotExists] to make the call a no-op on
// subsequent runs — useful for migrate-on-startup. The existing
// table's schema is NOT validated; use [Open] for strict matching.
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
//   - examples/features/search/fts-search — runnable demo of the raw fts.Index API.
//   - examples/features/gorm/fts-tagged — the same flow expressed via the
//     gorm bridge.
//   - dev/coverage/fts.md — every documented FTS5 feature with its
//     current status (typed / raw / inherited).
//   - github.com/go-again/sqlite/vfs/crypto — pure-Go encryption at
//     rest. Composes with FTS5 — encrypted full-text databases work
//     end-to-end through the same wrapping VFS. Same Recorder-shaped
//     observability surface, opted in via Options.Recorder.
package fts
