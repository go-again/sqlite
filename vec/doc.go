// Package vec adds first-class vector-search support to
// gosqlite.org by bundling the sqlite-vec extension and a small
// Go API layered on top of it.
//
// # Activating the extension
//
// Blank-importing this package auto-registers sqlite-vec on every database
// connection opened thereafter:
//
//	import (
//	    "database/sql"
//	    _ "gosqlite.org"
//	    _ "gosqlite.org/vec"
//	)
//
//	db, _ := sql.Open("sqlite3", ":memory:")
//	// Now CREATE VIRTUAL TABLE ... USING vec0(...) works.
//
// # Raw SQL usage (option a)
//
//	CREATE VIRTUAL TABLE docs USING vec0(embedding float[8]);
//	INSERT INTO docs(rowid, embedding) VALUES (1, '[0.1, 0.2, ...]');
//	SELECT rowid, distance
//	FROM   docs
//	WHERE  embedding MATCH '[0.5, 0.5, ...]'
//	ORDER BY distance LIMIT 5;
//
// To bind embeddings as parameters instead of hard-coding them, use
// [Encode] (or the methods on [Encoding]) so you get the right
// placeholder fragment + value pair for the configured wire format:
//
//	ph, val := vec.Encode(query, vec.Binary)
//	rows, _ := db.QueryContext(ctx,
//	    "SELECT rowid, distance FROM docs WHERE embedding MATCH "+ph+
//	        " ORDER BY distance LIMIT 5", val)
//
// # Typed Go API (option b)
//
// See Create, Open, and the Table type for an iter.Seq2-based KNN cursor and
// typed Insert/BatchInsert/Delete helpers that handle the wire encoding for
// you. The Encoding option selects both the column storage type and the bind
// form: JSON or Binary (float[N]), Int8 (int8[N], 4× smaller — quantized via
// vec_quantize_int8 assuming unit [-1, 1] range), or Bit (bit[N], 32× smaller,
// ranked by the Hamming metric Create forces). The typed API is built strictly
// on top of the raw SQL layer above — anything you can do in SQL you can do in
// Go.
//
// # Metadata, partition, and auxiliary columns
//
// Declare non-embedding columns via Options.Columns to filter, partition, and
// carry payloads on a vec0 table. A [Column] is Metadata (indexed, filterable),
// Partition (a partition-key shard, also filterable), or Auxiliary (an
// unindexed `+col` payload returned but not filterable). Set per-row values via
// [Row.Values] on [Table.InsertRow] / [Table.BatchInsert]; filter on metadata /
// partition columns in KNN with [WithFilter]; read metadata / auxiliary columns
// back with [WithSelect] + [Table.KNNSQL]. Options.ChunkSize sets vec0's
// chunk_size=.
//
// # Explicit primary keys (UUID / slug)
//
// [Table] addresses rows by the implicit int64 rowid. When your keys are
// strings (or you want an explicit key column), use [CreateKeyed] /
// [OpenKeyed] to get a [KeyedTable][K] (K = int64 | string): Insert / KNN take
// and return K-typed keys. [WithKeyColumn] names the key column (default "id").
// This is the form an ORM needs for models with non-int64 (UUID / slug)
// primary keys.
//
// # Filtered KNN
//
// [WithFilter] AND's a custom WHERE conjunct onto the MATCH clause.
// Same trust contract as [gorm.DB.Where] — the fragment is interpolated
// as-is; pass literals through args. Use it for per-tenant gating,
// rowid sub-selection, or any other column-level filter on the vec0
// table.
//
// # Custom projection / JOIN
//
// When you need to project additional columns or JOIN a companion
// table (the common "sidecar to canonical row table" pattern), use
// [Table.KNNSQL] with [WithSelect], [WithJoin], and [WithOrderBy] to
// build the query, then execute it through `db.QueryContext` (or
// `gorm.DB.Raw(sql, args...).Scan(&out)`):
//
//	sql, args, _ := tbl.KNNSQL(query, 10,
//	    vec.WithSelect("items.id, items.title"),
//	    vec.WithJoin("JOIN items ON items.id = items_vec.rowid"),
//	    vec.WithFilter("items.tenant = ?", "acme"),
//	)
//	rows, _ := db.QueryContext(ctx, sql, args...)
//
// [Table.KNN] / [Table.KNNSlice] reject WithSelect / WithJoin because
// they change the row shape the typed scanner expects.
//
// Note: when a JOIN is in play, KNNSQL emits `k = N` as a literal
// alongside the MATCH conjunct rather than relying on `LIMIT N`.
// sqlite-vec's planner doesn't extract LIMIT through a join boundary,
// but the `k = N` predicate is a vec0-recognized vtab hint that
// survives. Callers writing raw SQL by hand against vec0 should follow
// the same pattern — parameterizing `k` with a `?` placeholder will
// silently return an unbounded scan.
//
// # Idempotent migrations
//
// [Create] errors with [ErrAlreadyExists] (wrapped) if the table
// already exists. Pass [WithIfNotExists] to make the call a no-op on
// subsequent runs — useful for migrate-on-startup. The existing
// table's schema is NOT validated; use [Open] for strict matching.
//
// # Platform coverage
//
// The underlying transpiled vec extension is built per-target by
// modernc.org/sqlite/vec. Some GOOS/GOARCH combinations may not be supported
// upstream; see that package's build tags for the authoritative list.
//
// Practical consequence for downstream consumers: if you compile on a
// target the upstream `modernc.org/sqlite/vec` does not cover, `go
// build ./...` against your module will fail at this sub-package
// while the rest of gosqlite.org still compiles. The
// remaining sub-packages (root driver, fts, vfs, ext/) work
// on every supported target. Build with `go build ./... 2>/dev/null
// || go build ./` if you want to skip vec/ on niche arches, or list
// the packages you actually consume explicitly.
//
// # Observability
//
// Insert / BatchInsert / Update / Delete / KNN can be wrapped with slog
// logging or a metrics recorder by composing the optional decorators in
// observability.go: Wrap, WithLogger, WithRecorder. Parallel to the
// matching surface in gosqlite.org/fts.
//
// # See also
//
//   - examples/features/search/vec-search — runnable demo of the raw vec.Table API.
//   - LiteORM (liteorm.org) — an ORM with native, tag-driven vector search built on
//     this vec primitive (declarative vec: tags, AutoMigrate-provisioned
//     sidecars, typed ranked results).
//   - dev/coverage/vec.md — every documented sqlite-vec feature
//     with its current status (typed / raw / inherited).
//   - gosqlite.org/vfs/crypto — pure-Go encryption at
//     rest. Composes with vec0 — encrypted vector databases work
//     end-to-end through the same wrapping VFS. Same Recorder-shaped
//     observability surface, opted in via Options.Recorder.
package vec
