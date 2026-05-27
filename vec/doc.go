// Package vec adds first-class vector-search support to
// github.com/go-again/sqlite by bundling the sqlite-vec extension and a small
// Go API layered on top of it.
//
// # Activating the extension
//
// Blank-importing this package auto-registers sqlite-vec on every database
// connection opened thereafter:
//
//	import (
//	    "database/sql"
//	    _ "github.com/go-again/sqlite"
//	    _ "github.com/go-again/sqlite/vec"
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
// # Typed Go API (option b)
//
// See Create, Open, and the Table type for an iter.Seq2-based KNN cursor and
// typed Insert/BatchInsert/Delete helpers that handle JSON vs. binary
// encoding for you. The typed API is built strictly on top of the raw SQL
// layer above — anything you can do in SQL you can do in Go.
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
// while the rest of github.com/go-again/sqlite still compiles. The
// remaining sub-packages (root driver, fts, gorm, vfs, fts/gorm) work
// on every supported target. Build with `go build ./... 2>/dev/null
// || go build ./` if you want to skip vec/ on niche arches, or list
// the packages you actually consume explicitly.
//
// # Observability
//
// Insert / BatchInsert / Update / Delete / KNN can be wrapped with slog
// logging or a metrics recorder by composing the optional decorators in
// observability.go: Wrap, WithLogger, WithRecorder. Parallel to the
// matching surface in github.com/go-again/sqlite/fts.
//
// # See also
//
//   - github.com/go-again/sqlite/vec/gorm — tag-driven sqlite-vec
//     sidecars wired into gorm models. Define an Embedding field on
//     a gorm model and the plugin owns CRUD sync, soft-delete
//     filtering, and DropTable cascade.
//   - examples/vec-search — runnable demo of the raw vec.Table API.
//   - examples/gorm-vec-tagged — the same data flow expressed via
//     the gorm bridge.
//   - docs/coverage-vec.md — every documented sqlite-vec feature
//     with its current status (typed / raw / inherited).
package vec
